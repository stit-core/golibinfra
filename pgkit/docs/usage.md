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
    telemetry.Module, // даёт metric.Meter → включает db_pool_*
    pgkit.Module,
    fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig),

    fx.Invoke(func(db *pgkit.DB) {
        // db готов к Exec/Query/QueryRow
    }),
)
```

Без `telemetry.Module` (нет `metric.Meter` в графе) `Module` поднимается, но
инструментация пула — no-op.

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
- Вложенный `WithTx` (когда `ctx` уже несёт транзакцию) участвует во внешней
  транзакции, не открывая новую и не создавая savepoint.
- `InTx(ctx) bool` — проверить, выполняется ли код внутри транзакции (нужно
  коду, обязанному быть атомарным с бизнес-записью, например outbox-insert).

Изоляция новой транзакции — `ReadCommitted`. Для конфликтов сериализации/дедлоков
повторяй операцию по `IsRetryable` (см. ниже).

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

## Миграции

### На старте приложения (рекомендуемо)

`MigrateModule` прогоняет `MigrateUp` под advisory-lock в `OnStart`-хуке, до
обслуживания трафика. DSN берётся из `PoolConfig`, путь — из `PG_MIGRATIONS_PATH`
(default `/migrations`).

```go
fx.New(
    pgkit.Module,
    pgkit.MigrateModule,
    fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig),
    // ...
)
```

Advisory-lock гарантирует, что при одновременном старте нескольких инстансов
миграции применяет ровно один — остальные ждут, а не падают на гонке.

### Программно

```go
err := pgkit.MigrateUp(pgkit.MigrateConfig{DSN: dsn, Path: "./migrations"})
v, dirty, err := pgkit.MigrateVersion(pgkit.MigrateConfig{DSN: dsn, Path: "./migrations"})
err = pgkit.MigrateDown(pgkit.MigrateConfig{DSN: dsn, Path: "./migrations"}) // один шаг назад
```

`MigrateUp` молча игнорирует «нечего применять». `dirty=true` означает прерванную
миграцию — нужна ручная починка (функция вернёт ошибку вместе с версией).

### Несколько источников миграций

Когда схему дают и kit (через `embed.FS`), и приложение (`os.DirFS`):

```go
cfg := pgkit.MultiSourceConfig{
    DSN: dsn,
    Sources: []pgkit.Source{
        {Name: "kit",  FS: kitFS, Prefix: "migrations"},
        {Name: "app",  FS: os.DirFS(appPath), Prefix: "."},
    },
}
err := pgkit.MigrateUpMulti(cfg)
```

Composer склеивает все источники в один tmp-каталог с глобальной перенумерацией
`1..N`. **Порядок `Sources` — append-only**: новый источник добавляется только в
конец, иначе сдвинутся уже применённые версии в `schema_migrations`. Каждая
миграция обязана иметь и `.up.sql`, и `.down.sql`.

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

## Health-probe

```go
probe := pgkit.DBProbe(db) // func(ctx) error, выполняет SELECT 1
err := probe(ctx)
```

Подключай к readiness-эндпоинту потребителя.
