package bruteforcekit

import (
	"context"
	"time"
)

// LockState describes an active lock for a (subject, ip) pair.
// A nil *LockState returned by Guard.Check means "not locked".
type LockState struct {
	Until time.Time
}

// Guard provides generic anti-bruteforce protection for any subject
// (login / email / phone / etc.) bound to a client IP.
type Guard interface {
	// Check returns the active lock state for (subject, ip), or nil if not locked.
	Check(ctx context.Context, subject, ip string) (*LockState, error)
	// RecordFailure increments the failure counter; once it reaches the
	// configured threshold, a lock is engaged for Config.LockDuration.
	RecordFailure(ctx context.Context, subject, ip string) error
	// RecordSuccess clears the failure counter and any lock for (subject, ip).
	RecordSuccess(ctx context.Context, subject, ip string) error
}
