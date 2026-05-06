# bruteforcekit

Generic anti-bruteforce guard for any `(subject, ip)` pair — the subject is
opaque (email, phone, login, API-key id, etc.).

## API

```go
type Guard interface {
    Check(ctx, subject, ip) (*LockState, error)   // nil = not locked
    RecordFailure(ctx, subject, ip) error
    RecordSuccess(ctx, subject, ip) error
}
```

`Check` returns `nil` when there is no active lock; otherwise `*LockState`
with `Until` set to the lock expiry.

## Wiring

```go
import "git.stit.tech/akadmin/golibinfra/bruteforcekit"

app := fx.New(
    pgkit.Module,
    bruteforcekit.Module,
    fx.Invoke(func(g bruteforcekit.Guard) { /* ... */ }),
)
```

The Postgres implementation uses `*pgkit.DB`, so calls inside a
`pgkit.Transactor.WithTx` automatically participate in the surrounding
transaction.

## Config (envconfig)

| env                        | default | meaning                              |
|----------------------------|---------|--------------------------------------|
| `BRUTEFORCE_MAX_FAILURES`  | `5`     | failures before lock                 |
| `BRUTEFORCE_LOCK_DURATION` | `15m`   | how long the lock stays              |
| `BRUTEFORCE_WINDOW`        | `1h`    | reserved for sliding-window reset    |

## Migrations

`bruteforcekit.Migrations` is an `embed.FS` containing
`0001_bruteforce_attempts.{up,down}.sql`. Apply via your `cmd/migrate`
job alongside other module migrations.

## Test

```
go test ./bruteforcekit/...           # unit only
go test -tags=integration ./...       # full (testcontainers Postgres)
```
