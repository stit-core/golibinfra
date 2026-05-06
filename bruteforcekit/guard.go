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
}
