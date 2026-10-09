# pgkit — архитектура

Фасад поверх [pgx/v5](https://github.com/jackc/pgx): пул соединений
(`pgxpool`), `Transactor` с проброской транзакции через `context`, migrate-runner
поверх [golang-migrate](https://github.com/golang-migrate/migrate), маппинг
pg-ошибок в булевы предикаты, health-probe и опциональная read-replica. Пул
инструментируется метриками `pg_pool_*` и спанами через
[otelpgx](https://github.com/exaring/otelpgx).

Kit даёт сервису соединение, надёжность пула и атомарность записи; SQL, схема и
доменные репозитории — на стороне потребителя.

## Слои

```
PoolConfig ──► NewPool ──► *pgxpool.Pool ──┬─► DB (фасад Exec/Query/QueryRow)
                                           ├─► PoolTransactor (Transactor)
                                           └─► InstrumentPool (pg_pool_*{role})

ReplicaConfig ──► ReadWriteDB { Master, Replica }
```

`*pgxpool.Pool` — единственный «тяжёлый» ресурс; всё остальное (`DB`,
`PoolTransactor`, `ReadWriteDB`) — тонкие обёртки поверх него.

## Пул соединений (`pool.go`)

`NewPool(ctx, PoolConfig)` проверяет конфиг (`Validate`, ошибка обёрнута
`ErrConfig`), парсит DSN через `pgxpool.ParseConfig`, накладывает
размеры/таймауты пула и возвращает уже проверенный `Ping`-ом пул. `ctx` живёт
вместе с пулом: на нём в фоне прогреваются `MinConns`. `ConnectTimeout`
ограничивает установку каждого физического соединения
(`ConnConfig.ConnectTimeout`) и стартовый `Ping`. Если `OTelTrace` включён
(default), на `ConnConfig.Tracer` ставится `otelpgx.NewTracer()` — это даёт
спаны запросов, connect и acquire.

Нулевое значение размера или таймаута пула означает **дефолт pgxpool**: оно не
переносится в `pgxpool.Config`. WHY: перенесённые нули ломают пул изнутри —
`HealthCheckPeriod = 0` роняет процесс паникой `time.NewTicker(0)` из фоновой
горутины, `MaxConnLifetime = 0` протухает каждое соединение к первому
health-check'у, `MaxConns = 0` отвергается puddle. `PoolConfig{DSN: dsn}` обязан
быть рабочим пулом, а не миной.

Сессионные таймауты (`statement_timeout`, `idle_in_transaction_session_timeout`,
`lock_timeout`) переводятся в миллисекунды и кладутся в
`ConnConfig.RuntimeParams` — они уезжают в StartupMessage при установке
соединения. Ноль уезжает как `0` — без лимита, поверх `ALTER ROLE` и `ALTER DATABASE
… SET`; отрицательное значение отвергает `Validate`.

WHY `RuntimeParams`, а не `SET` в `AfterConnect`: `SET` стоит трёх round-trip'ов
на каждое новое физическое соединение (ротация по `MaxConnLifetime`,
реконнекты), а StartupMessage — ноль. WHY не параметры DSN: таймауты задаются
как `time.Duration` в конфиге, то есть остаются частью ENV-контракта, а не
«магией» внутри строки подключения. За PgBouncer в `transaction`-режиме
сессионные параметры не гарантируются.

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
    WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}
```

`PoolTransactor` реализует его поверх пула. Транзакция кладётся в `context` под
приватным ключом `txCtxKey{}`; извлекается она функцией `Conn` (см. выше).

Поведение `WithTx` / `WithTxOptions`:

- **Вложенность.** Если `ctx` уже несёт транзакцию, вызов просто выполняет
  `fn(ctx)` в той же транзакции — без `BEGIN`/`COMMIT` и без savepoint, под опциями
  внешней. Вложенный `WithTxOptions` строже внешней (изоляция выше — `RepeatableRead` или
  `Serializable` внутри `ReadCommitted`, `Serializable` внутри `RepeatableRead`; `ReadOnly`
  внутри `ReadWrite`) — `ErrNestedTxStricter` до `fn`: код, защищающий инвариант изоляцией
  или режимом, молча потерял бы защиту. Слабее или равный — участвует; пустой уровень —
  дефолт Postgres, `ReadCommitted`, как и `ReadUncommitted`. Маркер `WithTestTx` опций не
  несёт и не проверяется.
- **Изоляция.** `WithTx` открывает транзакцию с `pgx.TxOptions{IsoLevel:
  pgx.ReadCommitted}`; `WithTxOptions` — с переданными опциями (`Serializable`,
  `ReadOnly`, `Deferrable`).
- **Commit/Rollback.** Если `fn` вернула ошибку — `Rollback`; ошибка `fn`
  пробрасывается как есть (сохраняя sentinel'ы), а ошибка отката **присоединяется**
  через `errors.Join`, не подменяя причину. Не присоединяются два случая:
  `pgx.ErrTxClosed` (транзакция уже завершена) и отменённый `ctx` вызывающего —
  pgx уничтожает соединение при отмене запроса, поэтому «rollback: conn closed»
  там гарантирован и уводит от настоящей причины. Если `fn` успешна — `Commit` на
  `ctx` вызывающего: отменённый `ctx` означает «не коммитим».
- **Panic.** Откат подвешен в `defer`: panic внутри `fn` откатывает транзакцию,
  возвращает соединение в пул и продолжает раскрутку стека. Откат идёт на
  `context.WithoutCancel(ctx)`, чтобы отменённый `ctx` не оставил соединение в
  транзакции.

Вспомогательные функции:

- `Conn(ctx, pool) DBTX` — выбирает «исполнителя» запроса: транзакцию из `ctx`
  или сам пул. `DBTX` — общий интерфейс `Exec`/`Query`/`QueryRow`.
- `InTx(ctx) bool` — сообщает, есть ли в `ctx` активная транзакция. Нужен коду,
  которому важна атомарность с бизнес-записью (например, outbox-insert, который
  обязан выполняться внутри той же транзакции, что и доменная запись).
- `WithTestTx(ctx) context.Context` — кладёт в `ctx` маркер транзакции без
  `pgx.Tx`: `InTx` его видит, `Conn` игнорирует (запрос уходит в пул). Его ставит транзакция
  фейков `Transactor` (`testkit/pgtest`), чтобы код с гейтом на `InTx` тестировался без
  Postgres; фейки testkit отличают её от метки, поставленной самим тестом (`pgtest.InTx`).

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
| `IsLockNotAvailable(err)` | SQLSTATE `55P03` — `NOWAIT`/`SKIP LOCKED`-запрос или `lock_timeout` не дождались блокировки |
| `IsQueryCanceled(err)` | SQLSTATE `57014` — `statement_timeout` или `pg_cancel_backend` прервали запрос |

Код извлекается из `*pgconn.PgError` через `errors.AsType`; для не-pg или `nil`
ошибок все предикаты возвращают `false`.

WHY: бизнес-код не лезет в строковые SQLSTATE-коды и не зависит от внутренних
типов pgconn — он спрашивает «это нарушение уникальности?» / «эту операцию можно
повторить?». `IsRetryable` отделяет транзиентные конфликты (которые имеет смысл
повторить с backoff) от логических нарушений (которые повторять бессмысленно).
`IsLockNotAvailable` нужен протоколам «кто первый, тот и работает» (`FOR UPDATE
NOWAIT`), `IsQueryCanceled` — чтобы отличить срабатывание `statement_timeout` от
прочих сбоев запроса.

## Migrate-runner

### Источник миграций (`migrate.go`)

```go
type Source struct {
    Name string // ^[a-z][a-z0-9_]{0,44}$ — суффикс таблицы версий
    FS   fs.FS  // embed.FS кита или os.DirFS каталога приложения
    Dir  string // каталог внутри FS с *.up.sql/*.down.sql; "." для os.DirFS
}
func DirSource(name, path string) Source // os.DirFS(path), Dir "."
func (s Source) VersionTable() string   // "schema_migrations_" + Name
```

Имя источника — не длиннее **45 символов**: `schema_migrations_<Name>` должен
уложиться в 63 байта идентификатора Postgres. Нарушение регекспа — ошибка
`pgkit: migrate: invalid source name %q` на прогоне.

У каждого источника **своя таблица версий** `schema_migrations_<Name>`. Версии
источников независимы: новая миграция `0002` в `auditkit` применяется как
версия 2 источника `auditkit` и не затрагивает счётчик приложения. Каталог
приложения — источник `pgkit.AppSourceName` (`app`), таблица
`schema_migrations_app`.

WHY не одна общая таблица: golang-migrate ведёт единственный монотонный счётчик
на таблицу. Склеивание нескольких источников в один список требует глобальной
перенумерации, и добавление миграции в кит сдвигает номера приложения — migrate
затем пропускает или повторно применяет чужие файлы. Таблица на источник делает
каждый источник независимым `append-only`-логом.

`MigrateUp(ctx, dsn, lockTimeout, src)` / `MigrateDown` (один шаг) / `MigrateVersion`
работают с одним источником. Внутри: `source/iofs` читает `fs.FS` напрямую (без временных
каталогов), драйвер `database/pgx/v5` через `WithInstance` поверх `*sql.DB` из
`pgx/v5/stdlib` — на wire работает pgx, поэтому DSN может быть и URL, и
keyword/value, а `lib/pq` в зависимости не приходит. Соединение мигратора
использует simple protocol (файлы миграций многооператорные) и наследует таймаут
коннекта из дедлайна `ctx`. Пустой источник (нет `*.sql`) — ошибка: это почти
всегда неверный путь.

**Блокировки и дедлайн.** У соединения мигратора `lock_timeout` = `lockTimeout` (> 0;
`MigrateVersion` схему не меняет и его не ставит): DDL, упёршийся в чужую транзакцию, ждёт
блокировку не дольше него. WHY: ALTER в очереди за блокировкой держит за собой всех
читателей таблицы, и без предела одна долгая транзакция останавливает таблицу на всё своё
время. golang-migrate исполняет операторы на `context.Background()`, поэтому дедлайн `ctx`
доходит до них cancel request'ом серверу на каждую сессию мигратора — с повтором до конца
прогона, чтобы оператор, начатый уже после дедлайна (между миграциями), тоже не дошёл до
конца. Обрыв сокета не годится: оператор, ждущий блокировку, сокет не читает и продолжил бы
ждать. `GracefulStop` golang-migrate не годится: его флаг читают две горутины без
синхронизации, и остановка между миграциями теряется. Ошибка прерванного прогона несёт
`ctx.Err()` (`errors.Is(err, context.DeadlineExceeded)`). Упавшая на lock timeout или
дедлайне миграция оставляет версию `dirty=true`, как любой упавший файл.

**Проверка схемы на старте (`schema.go`).** `CheckSchema(ctx, db, src)` сверяет таблицу
версий источника с его последней миграцией (тем же разбором имён, что у golang-migrate):
нет таблицы, ни одной версии, версия ниже или `dirty` — `ErrSchema` с именем источника и
подсказкой «примените через `MigrateModule` или команду миграций».
`CheckSchemaOnStart(src)` вешает её на `OnStart` после optional `Migrated` с `*DB` из
графа — так хранилища китов (`auditkit`, `bruteforcekit`, `authnkit/session`) не стартуют
без своих миграций; `outboxkit/pgstore` сверяет схему таблицы перед созданием партиций, а
`outboxkit.Instance` — через `SchemaChecker` своего хранилища. WHY: легитимна сборка без
`MigrateModule` (миграции накатывает отдельная команда), и без проверки забытый источник
проявился бы `42P01` на первой записи в проде.

### Advisory-lock (`migrate_lock.go`)

`MigrateUpAll(ctx, dsn, lockTimeout, sources...)` применяет источники **по порядку** под одним
сессионным advisory-lock'ом `pg_advisory_lock` (ключ — FNV-1a от строки
`golibinfra-migrate`) на отдельном соединении; lock снимается в `defer`. Имена
источников проверяются на уникальность. WHY: при нескольких инстансах сервиса,
стартующих одновременно, миграции должен применять ровно один — остальные ждут
на advisory-lock и находят схему уже применённой, а не гонятся за неё.

Ожидание lock'а ограничено дедлайном `ctx`: `pg_advisory_lock` сам ждёт
бесконечно, поэтому дедлайн — единственный выход из очереди; `lockTimeout` к этому
соединению не относится — инстансы ждут мигрирующего, а не падают через секунды. У прямых
`MigrateUp*` дедлайн ставит вызывающий, у `MigrateModule` — `PG_MIGRATE_TIMEOUT`
(и `fx.StartTimeout` сверху). Инстанс, не дождавшийся lock'а, падает на старте
и уходит в рестарт — схему он не применяет.

### fx: `MigrateModule` и группа `pgkit.migrations` (`migrate_module.go`)

Киты регистрируют свой источник в fx-группе `pgkit.MigrationsGroup`
(`"pgkit.migrations"`): хранилища китов (`<kit>/pgstore`) — через
`pgkit.SupplyMigrations(src)` внутри своих `Module`, `outboxkit.Instance` — схему таблицы
инстанса.
`MigrateModule` в `OnStart` собирает группу, сортирует источники по имени (порядок
fx-группы не определён), добавляет последним каталог приложения из
`PG_MIGRATIONS_PATH` (если он не пуст) и вызывает `MigrateUpAll` под
`PG_MIGRATE_TIMEOUT` с `PG_MIGRATE_LOCK_TIMEOUT`. Приложение идёт последним, потому что его
схема может ссылаться на таблицы китов.

`OnStart`-хуки fx выполняются в порядке регистрации, а он зависит от порядка
модулей в `fx.New`. Чтобы схема не зависела от этого порядка, `MigrateModule`
провайдит маркер **`pgkit.Migrated`** (пустая структура): его конструктор и
вешает хук миграций. Модуль, которому схема нужна на старте, объявляет
`Migrated` зависимостью — fx строит маркер первым, а значит и хук миграций
встаёт в очередь раньше:

```go
type params struct {
    fx.In

    LC       fx.Lifecycle
    Store    *Store
    Migrated pgkit.Migrated `optional:"true"` // сборка без MigrateModule остаётся рабочей
}
```

Так сделано в `outboxkit/pgstore` (создание партиций) и в `CheckSchemaOnStart`.
`optional:"true"` нужен, чтобы приложение, накатывающее миграции отдельным шагом CI,
собиралось без `MigrateModule`.

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

## Health-пробы (`probe.go`)

`Probe(db)` (`Name` — `pgkit`) выполняет `SELECT 1` на пуле primary,
`ReplicaProbe(rw)` (`Name` — `pgkit/replica`) — на пуле реплики; без реплики
вторая проходит без запроса, потому что мастер уже покрыт первой. Обе
регистрируются в fx-группе `healthkit.probes` через `healthkit.ProvideProbe`; `httpkit`
собирает группу в `/ready`.

## fx-композиция (`module.go`, `migrate_module.go`)

`Module` provide'ит граф:

- `newPoolWithLifecycle` — primary `*pgxpool.Pool`: `NewPool` и хук — `OnStart` регистрирует
  метрики (`InstrumentPool(pool, RolePrimary)`), `OnStop` снимает регистрацию, закрывает пул и
  отменяет его контекст. WHY метрики в хуке, а не в конструкторе: `OnStop` fx зовёт только у
  хуков, до которых дошёл старт, и callback графа, который собран и не стартовал, остался бы в
  `MeterProvider`. `Close` ждёт возврата каждого занятого соединения, поэтому хук
  ждёт его две трети остатка бюджета остановки, затем пишет WARN `pgkit: pool close is still
  waiting for acquired connections` (`role`, `acquired_conns`) и возвращает `nil`. WHY:
  забытая транзакция не должна съесть бюджет хуков после пула — fx не зовёт хук, когда ctx
  остановки истёк, и телеметрия потеряла бы финальный экспорт; треть остатка — то же
  правило, что у `httpkit`.
- `NewDB` — фасад `*DB` поверх пула.
- `NewPoolTransactor` под интерфейсом `Transactor` (`fx.As`).
- `newReadWriteDB` — `*ReadWriteDB`; `ReplicaConfig` берётся как
  `optional:"true"`, поэтому сервис без реплики не обязан его провайдить. При
  пустом `PG_REPLICA_DSN` обе стороны указывают на мастер, иначе поднимается пул
  реплики (`PoolConfig` с DSN и `MaxConns` реплики, остальное наследуется) и
  инструментируется как `role=replica`.
- `fx.Invoke(func(*ReadWriteDB) {})` — пулы поднимаются на старте, а не при
  первом запросе: недоступная БД валит запуск приложения.

Метрики идут в глобальный `otel.Meter` (`internal/otelkit`): без
`MeterProvider` инструменты — no-op, `Module` при этом поднимается. Config'и
`Module` не provide'ит — это делает caller (App-структура или
`fx.Provide(pgkit.NewPoolConfig)`), единообразно с остальными китами; логгер —
`logkit.Logger` из графа.
`MigrateModule` — отдельный модуль (см. выше), ему нужны `PoolConfig` и
`MigrateRunnerConfig` (`pgkit.NewMigrateRunnerConfig`), а провайдит он
`pgkit.Migrated`.
