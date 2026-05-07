package txkit

import (
	"context"
	"sync/atomic"
)

type Identity struct{}

func (Identity) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type Recorder struct {
	calls atomic.Int64
}

func (r *Recorder) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.calls.Add(1)
	return fn(ctx)
}

func (r *Recorder) Calls() int { return int(r.calls.Load()) }
