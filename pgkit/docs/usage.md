# pgkit — рецепты

Сигнатуры даны как в коде. Идиоматичный путь — через `Module` (fx); прямые
конструкторы (`NewPool`, `NewDB`, `NewPoolTransactor`, `NewReadWriteDB`)
допустимы, но менее однородны с остальной библиотекой.

## Подключение через fx

`Module` provide'ит `*pgxpool.Pool`, `*pgkit.DB`, `pgkit.Transactor` и
`*pgkit.ReadWriteDB`; lifecycle закрывает пул(ы) на shutdown. Config'и
регистрирует caller.

```go
fx.New(
    pgkit.Module,
    fx.Provide(pgkit.NewPoolConfig),

    fx.Invoke(func(db *pgkit.DB) {
        // db готов к Exec/Query/QueryRow
    }),
)
```

`ReplicaConfig` — опциональная зависимость: добавляй
`fx.Provide(pgkit.NewReplicaConfig)` только там, где есть реплика (`PG_REPLICA_DSN`).

`pg_pool_*` идут в глобальный `otel.Meter`; без `MeterProvider` (потребитель не
подключил проводку OTel SDK) инструменты — no-op, `Module` при этом поднимается.

## Базовые запросы

```go
func (r *Repo) Count(ctx context.Context) (int, error) {
    var n int
    err := r.db.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
    return n, err
}
```

Каждый вызов `db.Exec/Query/QueryRow` идёт через `Conn(ctx, pool)`: внутри
`Transactor.WithTx` он автоматически использует транзакцию из `ctx`, вне её —
пул. Репозиторий пишется одинаково для обоих случаев.

## Транзакции через `Transactor`

```go
type Transactor interface {
    WithTx(ctx context.Context, fn func(ctx context.Context) error) error
    WithTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error
}
```

`fn` получает `ctx` с уже «вшитой» транзакцией. Любой `db.Exec(ctx, …)` внутри
`fn` попадает в неё.

```go
func (s *Service) Transfer(ctx context.Context, from, to int, amount int64) error {
    return s.tx.WithTx(ctx, func(ctx context.Context) error {
        if _, err := s.db.Exec(ctx,
            `UPDATE accounts SET balance = balance - $1 WHERE id = $2`, amount, from,
        ); err != nil {
            return err // → Rollback, ошибка проброшена как есть
        }
        if _, err := s.db.Exec(ctx,
            `UPDATE accounts SET balance = balance + $1 WHERE id = $2`, amount, to,
        ); err != nil {
            return err
        }
        return nil // → Commit
    })
}
```

- Вернуть ошибку из `fn` → `Rollback`; ошибка `fn` пробрасывается без обёртки
  (sentinel'ы сохраняются). Ошибка `Commit`/`Rollback` оборачивается
  `pgkit: …`.
- Panic внутри `fn` → `Rollback` и re-panic: соединение возвращается в пул,
  panic ловит вызывающий слой (`httpkit`/`timerkit` recover'ят свои обработчики).
- Вложенный вызов (когда `ctx` уже несёт транзакцию) участвует во внешней
  транзакции, не открывая новую и не создавая savepoint.
- `InTx(ctx) bool` — проверить, выполняется ли код внутри транзакции (нужно
  коду, обязанному быть атомарным с бизнес-записью, например outbox-insert). В
  тестах тот же гейт открывает `pgkit.WithTestTx(ctx)` — маркер без Postgres,
  который кладут фейки `testkit/pgtest`.
- Отмена `ctx` внутри `fn`: наружу уходит причина отмены; ошибка отката
  «мёртвого» соединения её не подменяет.

Изоляция `WithTx` — `ReadCommitted`. Другой уровень или read-only транзакция —
через `WithTxOptions`:

```go
err := s.tx.WithTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context) error {
    // ...
    return nil
})
```

Для конфликтов сериализации/дедлоков повторяй операцию по `IsRetryable` (см.
ниже).

## Обработка pg-ошибок

Предикаты распознают обёрнутые ошибки (`errors.As`/`errors.Is`):

```go
_, err := db.Exec(ctx, `INSERT INTO users(email) VALUES ($1)`, email)
switch {
case pgkit.IsUniqueViolation(err):
    return ErrEmailTaken
case pgkit.IsForeignKeyViolation(err):
    return ErrBadRef
case err != nil:
    return err
}
```

```go
err := db.QueryRow(ctx, `SELECT id FROM users WHERE email=$1`, email).Scan(&id)
if pgkit.IsNoRows(err) {
    return ErrNotFound
}
```

Повтор транзиентных конфликтов:

```go
for attempt := 0; attempt < maxRetries; attempt++ {
    err = tx.WithTx(ctx, doWork)
    if !pgkit.IsRetryable(err) { // не serialization failure / deadlock
        break
    }
    // backoff перед повтором
}
```

«Кто первый, тот и работает» через `FOR UPDATE NOWAIT`:

```go
_, err := db.Exec(ctx, `SELECT id FROM jobs WHERE id = $1 FOR UPDATE NOWAIT`, id)
if pgkit.IsLockNotAvailable(err) {
    return nil // строку держит другой инстанс — пропускаем
}
```

`pgkit.IsQueryCanceled(err)` отличает срабатывание `statement_timeout` от
прочих сбоев запроса.

## Миграции

### На старте приложения (рекомендуемо)

`MigrateModule` в `OnStart` под advisory-lock применяет источники китов из
fx-группы `pgkit.migrations` (их регистрируют `Module` китов —
`auditkit/pgstore`, `outboxkit/pgstore`, `bruteforcekit/pgstore` — и
`outboxkit.Instance`), затем каталог приложения из `PG_MIGRATIONS_PATH`
(default `/migrations`; `-` — у приложения своих миграций нет). DSN
берётся из `PoolConfig`.

```go
fx.New(
    pgkit.Module,
    pgkit.MigrateModule,
    fx.Provide(pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig),
    auditpg.Module, // регистрирует auditkit-миграции в группе pgkit.migrations
    // ...
)
```

У каждого источника своя таблица версий: `schema_migrations_app` для
приложения, `schema_migrations_<kit>` для китов. Advisory-lock гарантирует, что
при одновременном старте нескольких инстансов миграции применяет ровно один —
остальные ждут его в очереди до `PG_MIGRATE_TIMEOUT` (default 2m) и находят
схему уже применённой; не дождавшийся инстанс падает на старте и уходит в
рестарт. `fx.StartTimeout` должен покрывать `PG_MIGRATE_TIMEOUT` плюс остальной
старт — иначе прогон обрывает fx, а прерванная миграция остаётся `dirty`
(см. [configuration.md](configuration.md)).

Хуку `OnStart`, которому нужна применённая схема, объяви зависимость от
`pgkit.Migrated` — тогда порядок не зависит от позиции модуля в `fx.New`:

```go
type params struct {
    fx.In

    LC       fx.Lifecycle
    Migrated pgkit.Migrated `optional:"true"`
}
```

Свой источник в группу (например, миграции внутреннего пакета приложения):

```go
pgkit.ProvideMigrations(pgkit.Source{Name: "billing", FS: billingFS, Dir: "migrations"})
```

### Программно

```go
src := pgkit.DirSource(pgkit.AppSourceName, "./migrations")
err := pgkit.MigrateUp(ctx, dsn, src)
v, dirty, err := pgkit.MigrateVersion(ctx, dsn, src)
err = pgkit.MigrateDown(ctx, dsn, src) // один шаг назад

err = pgkit.MigrateUpAll(ctx, dsn, auditpg.Migrations, outboxpg.Migrations, src) // под advisory-lock, по порядку
```

`MigrateUp` молча игнорирует «нечего применять». `dirty=true` означает прерванную
миграцию — нужна ручная починка (функция вернёт ошибку вместе с версией).
Ручная работа через CLI golang-migrate — с параметром
`x-migrations-table=schema_migrations_<name>` в DSN.

**Дедлайн — ответственность вызывающего.** Прямые `MigrateUp*` уважают только
`ctx`, который им передали: он ограничивает коннект и ожидание advisory-lock'а.
`MigrateUpAll(context.Background(), …)` из CLI будет ждать lock бесконечно —
оборачивай в `context.WithTimeout`.

## Read-replica

```go
type ReadWriteDB struct {
    Master  *DB
    Replica *DB
}
func (rw *ReadWriteDB) Read() *DB  // реплика (или мастер, если реплики нет)
func (rw *ReadWriteDB) Write() *DB // всегда мастер
```

```go
func (r *Repo) ListPublic(ctx context.Context) ([]Item, error) {
    return queryItems(ctx, r.rw.Read()) // терпит репликационный лаг
}

func (r *Repo) Create(ctx context.Context, it Item) error {
    _, err := r.rw.Write().Exec(ctx, `INSERT INTO items ...`, it.ID)
    return err
}
```

Роутинг **explicit** — kit не парсит SQL. Если `PG_REPLICA_DSN` пуст, `Read()`
тоже идёт на мастер, поэтому read/write-split-код работает и без реплики. Не
читай через `Read()` то, что только что записал через `Write()` (read-after-write
не гарантируется из-за лага репликации) — такие чтения веди через `Write()`.

## Health-пробы

```go
func Probe(db *DB) healthkit.Probe            // Name "pgkit": SELECT 1 на primary
func ReplicaProbe(rw *ReadWriteDB) healthkit.Probe // Name "pgkit/replica": SELECT 1 на реплике
```

```go
fx.New(
    pgkit.Module,
    httpkit.Module,
    fx.Provide(pgkit.NewPoolConfig, httpkit.NewConfig),

    healthkit.Provide(pgkit.Probe),      // /ready → 503, если SELECT 1 не проходит
    healthkit.Provide(pgkit.ReplicaProbe), // без PG_REPLICA_DSN проходит без запроса
)
```
