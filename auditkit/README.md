# auditkit

Generic audit log for mutating operations. Records actor, action, target resource and metadata into a `audit_log` table in Postgres.

## API

```go
type Entry struct {
    ActorID    string
    Action     string
    Resource   string
    ResourceID string
    Metadata   map[string]any
    At         time.Time
    IP         string
    RequestID  string
}

type Logger interface {
    Log(ctx context.Context, e Entry) error
}
```

`NewPgLogger(db *pgkit.DB) Logger` — Postgres implementation.

## Migration

Embedded under `auditkit.Migrations` (`embed.FS`). Apply via your migration runner (e.g. `pgkit.MigrateUp`) by extracting the FS to disk, or include it in your project migrations.

```sql
-- migrations/0001_audit_log.up.sql
CREATE TABLE audit_log ( ... );
```

## FX wiring

```go
import "git.stit.tech/akadmin/golibinfra/auditkit"

fx.New(
    pgkit.Module,
    auditkit.Module, // provides auditkit.Logger
    fx.Invoke(func(l auditkit.Logger) { /* ... */ }),
)
```

## Usage

```go
_ = logger.Log(ctx, auditkit.Entry{
    ActorID:    user.ID,
    Action:     "user.update",
    Resource:   "user",
    ResourceID: user.ID,
    Metadata:   map[string]any{"fields": []string{"email"}},
    RequestID:  reqID(ctx),
})
```
