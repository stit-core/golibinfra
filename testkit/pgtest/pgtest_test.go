package pgtest_test

import (
	"context"
	"errors"
	"slices"
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

// Ошибка и panic fn откатывают записи фейков, успех — нет; откат — в обратном порядке записей.
func TestIdentity_RollbackUndoesFakeWrites(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for name, c := range map[string]struct {
		fn       func(ctx context.Context) error
		wantUndo []int
	}{
		"error":   {func(context.Context) error { return boom }, []int{2, 1}},
		"success": {func(context.Context) error { return nil }, nil},
		"panic":   {func(context.Context) error { panic(boom) }, []int{2, 1}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var undone []int
			func() {
				defer func() { _ = recover() }()
				_ = pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error {
					for i := range 2 {
						if !pgtest.OnRollback(ctx, func() { undone = append(undone, i+1) }) {
							t.Error("OnRollback in a fake transaction = false")
						}
					}
					return c.fn(ctx)
				})
			}()
			if !slices.Equal(undone, c.wantUndo) {
				t.Fatalf("undone = %v, want %v", undone, c.wantUndo)
			}
		})
	}
}

// Вложенный вызов идёт во внешней транзакции: её исход, а не ошибка вложенного, решает откат.
func TestIdentity_NestedJoinsOuter(t *testing.T) {
	t.Parallel()
	undone := false
	err := pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error {
		inner := (&pgtest.Recorder{}).WithTx(ctx, func(ctx context.Context) error {
			pgtest.OnRollback(ctx, func() { undone = true })
			return errors.New("handled by the caller")
		})
		if inner == nil {
			t.Error("nested error lost")
		}
		return nil
	})
	if err != nil || undone {
		t.Fatalf("err = %v, undone = %v, want the outer commit to keep the write", err, undone)
	}
}

// Транзакция фейка — не метка теста: InTx видит только её; вне неё отменять нечего.
func TestInTx_OnlyFakeTransaction(t *testing.T) {
	t.Parallel()
	if pgtest.InTx(pgkit.WithTestTx(t.Context())) {
		t.Error("InTx of a pgkit.WithTestTx marker = true")
	}
	if pgtest.OnRollback(pgkit.WithTestTx(t.Context()), func() {}) {
		t.Error("OnRollback outside a fake transaction = true")
	}
	_ = pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error {
		if !pgtest.InTx(ctx) || !pgkit.InTx(ctx) {
			t.Error("InTx inside the fake transaction = false")
		}
		return nil
	})
}

// Отменённый ctx — ошибка до fn, как BeginTx боевого Transactor.
func TestIdentity_CancelledCtxFailsBeforeFn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := pgtest.Identity{}.WithTx(ctx, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("err = %v, fn called = %v, want context.Canceled before fn", err, called)
	}
}
