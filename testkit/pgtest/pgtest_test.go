package pgtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

var (
	_ pgkit.Transactor = pgtest.Identity{}
	_ pgkit.Transactor = (*pgtest.Recorder)(nil)
)

func TestIdentity_RunsFnAndPropagatesError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	got := pgtest.Identity{}.WithTx(context.Background(), func(context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("err=%v want %v", got, want)
	}
}

func TestRecorder_CountsCalls(t *testing.T) {
	t.Parallel()
	r := &pgtest.Recorder{}
	for range 3 {
		_ = r.WithTx(context.Background(), func(context.Context) error { return nil })
	}
	if r.Calls() != 3 {
		t.Fatalf("calls=%d want 3", r.Calls())
	}
}

func TestWithTxOptions_IgnoresOptionsAndRunsFn(t *testing.T) {
	t.Parallel()
	opts := pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly}
	want := errors.New("boom")

	if got := (pgtest.Identity{}).WithTxOptions(t.Context(), opts,
		func(context.Context) error { return want },
	); !errors.Is(got, want) {
		t.Fatalf("Identity.WithTxOptions err = %v, want %v", got, want)
	}

	r := &pgtest.Recorder{}
	called := false
	if err := r.WithTxOptions(t.Context(), opts, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("Recorder.WithTxOptions: %v", err)
	}
	if !called || r.Calls() != 1 {
		t.Fatalf("called = %v, calls = %d, want true/1", called, r.Calls())
	}
}

// Гейт на pgkit.InTx (например outboxkit/pgstore с WithRequireTx) должен
// проходить под фейками — иначе абстракция Transactor протекает.
func TestFakes_MarkCtxAsInTx(t *testing.T) {
	t.Parallel()
	fakes := map[string]pgkit.Transactor{
		"Identity": pgtest.Identity{},
		"Recorder": &pgtest.Recorder{},
	}
	for name, tr := range fakes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := tr.WithTx(t.Context(), func(ctx context.Context) error {
				if !pgkit.InTx(ctx) {
					t.Error("WithTx: InTx(ctx) = false")
				}
				return nil
			}); err != nil {
				t.Fatalf("WithTx: %v", err)
			}
			if err := tr.WithTxOptions(t.Context(), pgx.TxOptions{}, func(ctx context.Context) error {
				if !pgkit.InTx(ctx) {
					t.Error("WithTxOptions: InTx(ctx) = false")
				}
				return nil
			}); err != nil {
				t.Fatalf("WithTxOptions: %v", err)
			}
		})
	}
}
