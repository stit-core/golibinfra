package bruteforcetest

import (
	"context"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

type Permissive struct{}

func (Permissive) Check(context.Context, string, string) (*bruteforcekit.LockState, error) {
	return nil, nil
}
func (Permissive) RecordFailure(context.Context, string, string) error { return nil }
func (Permissive) RecordSuccess(context.Context, string, string) error { return nil }
func (Permissive) Cleanup(context.Context) error                       { return nil }

type Locked struct {
	Until time.Time
}

func (l Locked) Check(context.Context, string, string) (*bruteforcekit.LockState, error) {
	return &bruteforcekit.LockState{Until: l.Until}, nil
}
func (Locked) RecordFailure(context.Context, string, string) error { return nil }
func (Locked) RecordSuccess(context.Context, string, string) error { return nil }
func (Locked) Cleanup(context.Context) error                       { return nil }

type Recorder struct {
	mu        sync.Mutex
	failures  []string
	successes []string
}

func (r *Recorder) Check(context.Context, string, string) (*bruteforcekit.LockState, error) {
	return nil, nil
}

func (r *Recorder) RecordFailure(_ context.Context, subject, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, subject)
	return nil
}

func (r *Recorder) RecordSuccess(_ context.Context, subject, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.successes = append(r.successes, subject)
	return nil
}

func (r *Recorder) Failures() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.failures))
	copy(out, r.failures)
	return out
}

func (r *Recorder) Successes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.successes))
	copy(out, r.successes)
	return out
}

func (r *Recorder) Cleanup(context.Context) error { return nil }
