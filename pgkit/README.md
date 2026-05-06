# pgkit

Postgres primitives: pool, transactor, error helpers, migration runner.

## What it provides

- `NewPool(ctx, cfg)` → `*pgxpool.Pool` with sensible defaults and a startup ping
- `PoolTransactor` implementing `Transactor` — `WithTx(ctx, fn)` with automatic context propagation
- `Conn(ctx, pool)` — returns the active tx if present, else the pool (use this in repos)
- Error helpers: `IsNoRows`, `IsUniqueViolation`, `IsForeignKeyViolation`, `IsCheckViolation`, `IsNotNullViolation`, `IsRetryable`
- `MigrateUp` / `MigrateDown` / `MigrateVersion` — golang-migrate wrappers
- `Module` — fx module providing `*pgxpool.Pool` + `Transactor`

## Usage

```go
// product config embeds pgkit.PoolConfig
type Config struct {
    pgkit.PoolConfig
    // ... other product fields
}

// in main.go
fx.New(
    fx.Provide(func(c *Config) pgkit.PoolConfig { return c.PoolConfig }),
    pgkit.Module,
    // ...
).Run()

// in repository
type Repository struct{ pool *pgxpool.Pool }

func (r *Repository) Create(ctx context.Context, u *user.User) error {
    err := r.pool.QueryRow(ctx, `INSERT ...`).Scan(...)
    if pgkit.IsUniqueViolation(err) {
        return user.ErrAlreadyExists
    }
    return err
}
```

## Conventions

- Repositories take `*pgxpool.Pool` as a constructor dep, not `Transactor`. To run inside a tx, the **usecase** wraps the call in `transactor.WithTx(ctx, fn)`.
- For tx-aware repos, replace `r.pool` with `pgkit.Conn(ctx, r.pool)` — automatic dispatch.
- Don't catch `pgx.ErrNoRows` — use `pgkit.IsNoRows(err)` to keep imports stable.
