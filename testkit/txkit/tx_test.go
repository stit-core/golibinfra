package txkit_test

import (
	"context"
	"errors"
	"testing"

	"git.stit.tech/stit/golibinfra/pgkit"
	"git.stit.tech/stit/golibinfra/testkit/txkit"
)

var (
	_ pgkit.Transactor = txkit.Identity{}
	_ pgkit.Transactor = (*txkit.Recorder)(nil)
)

func TestIdentity_RunsFnAndPropagatesError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	got := txkit.Identity{}.WithTx(context.Background(), func(context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("err=%v want %v", got, want)
	}
}

func TestRecorder_CountsCalls(t *testing.T) {
	t.Parallel()
	r := &txkit.Recorder{}
	for range 3 {
		_ = r.WithTx(context.Background(), func(context.Context) error { return nil })
	}
	if r.Calls() != 3 {
		t.Fatalf("calls=%d want 3", r.Calls())
	}
}
