package bruteforcekit

import (
	"context"
	"time"
)

type LockState struct {
	Until time.Time
}

type Guard interface {
	Check(ctx context.Context, subject, ip string) (*LockState, error)

	RecordFailure(ctx context.Context, subject, ip string) error

	RecordSuccess(ctx context.Context, subject, ip string) error

	// Cleanup deletes expired attempt rows (unlocked AND older than
	// Config.CleanupRetention). Callers typically schedule it via a periodic
	// job. Idempotent.
	Cleanup(ctx context.Context) error
}
