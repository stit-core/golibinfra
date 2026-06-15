# pgkit — архитектура

Фасад поверх [pgx/v5](https://github.com/jackc/pgx): пул соединений
(`pgxpool`), `Transactor` с проброской транзакции через `context`, migrate-runner
поверх [golang-migrate](https://github.com/golang-migrate/migrate), маппинг
pg-ошибок в булевы предикаты, health-probe и опциональная read-replica. Пул
инструментируется метриками `db_pool_*` и per-query спанами через
[otelpgx](https://github.com/exaring/otelpgx).

Kit даёт сервису соединение, надёжность пула и атомарность записи; SQL, схема и
доменные репозитории — на стороне потребителя.

## Слои

```
PoolConfig ──► NewPool ──► *pgxpool.Pool ──┬─► DB (фасад Exec/Query/QueryRow)
                                           ├─► PoolTransactor (Transactor)
                                           └─► InstrumentPool (db_pool_*)

ReplicaConfig ──► ReadWriteDB { Master, Replica }
```

`*pgxpool.Pool` — единственный «тяжёлый» ресурс; всё остальное (`DB`,
`PoolTransactor`, `ReadWriteDB`) — тонкие обёртки поверх него.

## Пул соединений (`pool.go`)

`NewPool(ctx, PoolConfig)` парсит DSN через `pgxpool.ParseConfig`, накладывает
размеры/таймауты пула и возвращает уже проверенный `Ping`-ом пул. Если
`OTelTrace` включён (default), на `ConnConfig.Tracer` ставится
`otelpgx.NewTracer()` — это даёт per-query спаны.

Сессионные таймауты (`statement_timeout`, `idle_in_transaction_session_timeout`,
`lock_timeout`) применяются в `AfterConnect`-хуке: на каждое новое физическое
соединение выполняется `SET <name> = '<ms>ms'`. Хук сохраняет ранее
установленный `AfterConnect` и вызывает его первым, чтобы не затирать кастомную
инициализацию из DSN. Отрицательные значения нормализуются в `0` (без лимита).

WHY именно `AfterConnect`, а не параметры DSN: таймауты задаются как
`time.Duration` в конфиге и переводятся в миллисекунды единообразно; это держит
их частью `PoolConfig` (а значит — частью ENV-контракта), а не «магией» внутри
строки подключения.

## Фасад `DB` (`db.go`)

`DB` оборачивает `*pgxpool.Pool` и проксирует три метода — `Exec`, `Query`,
`QueryRow`. Каждый вызов идёт не напрямую в пул, а через `Conn(ctx, pool)`:
если в `ctx` лежит активная транзакция (положенная `Transactor.WithTx`), запрос
выполняется в ней; иначе — берётся соединение из пула.

WHY: это делает репозиторий-код transaction-agnostic. Один и тот же
`db.Exec(ctx, …)` атомарно попадает в текущую транзакцию, когда вызван внутри
`WithTx`, и работает автономно вне её — репозиторию не нужно знать, открыта
транзакция или нет, и не нужно прокидывать `tx` отдельным аргументом.

Алиасы типов (`Row`, `Rows`, `Tx`, `NamedArgs`, `CommandTag`) реэкспортят pgx-/
pgconn-типы, чтобы потребитель не импортировал pgx напрямую для базовых сигнатур.

## `Transactor` (`transactor.go`)

```go
type Transactor interface {
    WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

`PoolTransactor` реализует его поверх пула. Транзакция кладётся в `context` под
приватным ключом `txCtxKey{}`; извлекается она функцией `Conn` (см. выше).

Поведение `WithTx`:

- **Вложенность.** Если `ctx` уже несёт транзакцию, `WithTx` просто вызывает
  `fn(ctx)` в той же транзакции — без `BEGIN`/`COMMIT`. Вложенный `WithTx`
  участвует в внешней транзакции, а не открывает savepoint.
- **Изоляция.** Новая транзакция открывается с `pgx.TxOptions{IsoLevel:
  pgx.ReadCommitted}`.
- **Commit/Rollback.** Если `fn` вернула ошибку — `Rollback`; ошибка `fn`
  пробрасывается как есть (сохраняя sentinel'ы), а ошибка отката добавляется
  обёрткой `pgkit: rollback: … (after: …)`, кроме `pgx.ErrTxClosed` (транзакция
  уже завершена — это не ошибка). Если `fn` успешна — `Commit`.

Вспомогательные функции:

- `Conn(ctx, pool) DBTX` — выбирает «исполнителя» запроса: транзакцию из `ctx`
  или сам пул. `DBTX` — общий интерфейс `Exec`/`Query`/`QueryRow`.
- `InTx(ctx) bool` — сообщает, есть ли в `ctx` активная транзакция. Нужен коду,
  которому важна атомарность с бизнес-записью (например, outbox-insert, который
  обязан выполняться внутри той же транзакции, что и доменная запись).

## Маппинг ошибок (`errors.go`)

Предикаты-обёртки над pg-кодами и `pgx.ErrNoRows`, работающие сквозь
`errors.As`/`errors.Is` (распознают обёрнутые ошибки):

| Предикат | Триггер |
|---|---|
| `IsNoRows(err)` | `errors.Is(err, pgx.ErrNoRows)` |
| `IsUniqueViolation(err)` | SQLSTATE `23505` |
| `IsForeignKeyViolation(err)` | SQLSTATE `23503` |
| `IsCheckViolation(err)` | SQLSTATE `23514` |
| `IsNotNullViolation(err)` | SQLSTATE `23502` |
| `IsRetryable(err)` | SQLSTATE `40001` (serialization failure) или `40P01` (deadlock detected) |

Код извлекается из `*pgconn.PgError` через `errors.AsType`; для не-pg или `nil`
ошибок все предикаты возвращают `false`.

WHY: бизнес-код не лезет в строковые SQLSTATE-коды и не зависит от внутренних
типов pgconn — он спрашивает «это нарушение уникальности?» / «эту операцию можно
повторить?». `IsRetryable` отделяет транзиентные конфликты (которые имеет смысл
повторить с backoff) от логических нарушений (которые повторять бессмысленно).

## Migrate-runner

Три слоя поверх golang-migrate:

1. **Базовый (`migrate.go`).** `MigrateUp`/`MigrateDown`/`MigrateVersion` поверх
   `MigrateConfig{DSN, Path}`. `Path` — каталог с файлами `*.up.sql`/`*.down.sql`.
   `MigrateUp` игнорирует `migrate.ErrNoChange` (нечего применять — не ошибка).
   `MigrateDown` откатывает ровно один шаг (`Steps(-1)`). `MigrateVersion`
   возвращает `(version, dirty, err)`: `ErrNilVersion` → `(0, false, nil)`,
   а `dirty=true` отдаётся вместе с ошибкой «нужна ручная починка».
   Импортируются драйверы `database/postgres` и `source/file`.

2. **Advisory-lock (`migrate_lock.go`).** `runMigrateUpLocked(ctx, dsn, path)`
   открывает отдельный пул `MaxConns=1`, берёт сессионный advisory-lock
   `pg_advisory_lock` (ключ — FNV-1a от строки `golibinfra-migrate`), прогоняет
   `MigrateUp` и снимает lock в `defer`. WHY: при нескольких инстансах сервиса,
   стартующих одновременно, миграции должен применять ровно один — остальные
   ждут на advisory-lock, а не падают на гонке по `schema_migrations`.

3. **Multi-source composer (`migrate_multi.go`).** `MigrateUpMulti` /
   `MigrateDownMulti` / `MigrateVersionMulti` поверх `MultiSourceConfig{DSN,
   Sources}`. Каждый `Source` (`Name`, `FS`, `Prefix`) — это `fs.FS`
   (`embed.FS` из kit'а или `os.DirFS` приложения). Composer собирает все пары
   `*.up.sql`/`*.down.sql` из всех источников и пишет их во временный каталог с
   **глобальной перенумерацией `1..N`**: сначала в порядке `Sources`, затем по
   версии внутри источника. Затем зовёт обычный `MigrateUp`/… на этом tmp-каталоге.

   Инвариант для прод-БД: порядок `Sources` в caller-коде **append-only**. Новый
   источник добавляется в конец списка, иначе сдвинутся уже применённые версии в
   `schema_migrations`. Файл без `.up.sql` или `.down.sql` — ошибка composer'а;
   пустой набор источников — тоже ошибка. Для локального dev (drop+recreate) сдвиг
   нумерации не критичен.

## Read-replica (`replica.go`)

```go
type ReadWriteDB struct {
    Master  *DB
    Replica *DB
}
```

`ReadWriteDB.Read()` отдаёт реплику, `Write()` — мастер. Если реплика не задана
(`NewReadWriteDB(master, nil)` или пустой `PG_REPLICA_DSN`), `Read()` тоже
возвращает мастер — код read/write-split пишется один раз и работает в обоих
топологиях.

WHY роутинг — explicit, а не автоматический: kit не парсит SQL и не решает за
вас, какой запрос куда. Потребитель сам выбирает `rw.Read()` для запросов,
которые терпят репликационный лаг, и `rw.Write()` — для всего остального
(включая read-after-write в рамках одной операции). Подробности и подводные
камни — в [usage.md § Read-replica](usage.md).

## Health-probe (`probe.go`)

`DBProbe(db) func(ctx) error` возвращает замыкание, выполняющее `SELECT 1`.
Замыкание совместимо с health-чеком потребителя (например, readiness-эндпоинтом
`httpkit`).

## fx-композиция (`module.go`, `migrate_module.go`)

`Module` provide'ит граф:

- `newPoolWithLifecycle` — `*pgxpool.Pool` + `OnStop`-хук, закрывающий пул и
  отменяющий его контекст при graceful shutdown.
- `NewDB` — фасад `*DB` поверх пула.
- `NewPoolTransactor` под интерфейсом `Transactor` (`fx.As`).
- `newReadWriteDB` — `*ReadWriteDB`; при пустом `PG_REPLICA_DSN` обе стороны
  указывают на мастер, иначе поднимается отдельный пул реплики с собственным
  lifecycle-хуком.
- `instrumentPoolFx` (через `fx.Invoke`) — вешает `db_pool_*` на пул, если в
  графе есть `metric.Meter`.

`Meter` помечен `optional:"true"`: без `telemetry.Module` инструментация
становится no-op, а не падает на старте.

**Asymmetry note.** Ни один kit golibinfra не provide'ит свой Config внутри
`Module` — это делает caller. Идиоматично — через App-структуру в app-config
модуле; дёшево — `fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig)`.
`MigrateModule` — отдельный модуль: он добавляет `OnStart`-хук, который
прогоняет `runMigrateUpLocked` до того, как приложение начнёт обслуживать
трафик (читает `PG_MIGRATIONS_PATH`).
