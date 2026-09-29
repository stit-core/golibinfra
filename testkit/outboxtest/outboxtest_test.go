package outboxtest_test

import (
	"context"
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/outboxtest"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

type narrowBus interface {
	Publish(ctx context.Context, subject string, payload any) error
}

var _ narrowBus = (*outboxtest.Recorder)(nil)

func TestRecorder_PublishAndRead(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	ctx := pgkit.WithTestTx(t.Context())
	if err := r.Publish(ctx, "user.created", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "user.created", "u2"); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "auth.login", "u1"); err != nil {
		t.Fatal(err)
	}

	if r.Count() != 3 {
		t.Fatalf("Count=%d want 3", r.Count())
	}
	got := r.Published("user.created")
	if len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("Published(user.created)=%v", got)
	}
	if r.Last("auth.login") != "u1" {
		t.Fatalf("Last(auth.login)=%v", r.Last("auth.login"))
	}
	if r.Last("missing") != nil {
		t.Fatalf("Last(missing) must be nil")
	}
	r.Reset()
	if r.Count() != 0 {
		t.Fatalf("Reset failed")
	}
}

func TestRecorder_AllReturnsCopyInOrder(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	ctx := pgkit.WithTestTx(t.Context())
	if err := r.Publish(ctx, "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(ctx, "b", 2); err != nil {
		t.Fatal(err)
	}

	all := r.All()
	if len(all) != 2 || all[0].Subject != "a" || all[1].Subject != "b" {
		t.Fatalf("All() = %+v", all)
	}
	all[0].Subject = "tampered"
	if r.All()[0].Subject != "a" {
		t.Error("All must return a copy of the slice")
	}
	if got := r.Published("missing"); got != nil {
		t.Errorf("Published(missing) = %v, want nil", got)
	}
}

// Регрессия D02/M34 аудита шаблона: usecase, публикующий мимо
// Transactor.WithTx, краснеет в тесте домена, а не проходит молча.
func TestRecorder_PublishRequiresTx(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	err := r.Publish(t.Context(), "note.created", "n1")
	if !errors.Is(err, outboxkit.ErrNoTx) {
		t.Fatalf("Publish outside tx: err = %v, want ErrNoTx", err)
	}
	if r.Count() != 0 {
		t.Fatalf("event recorded outside tx: Count = %d, want 0", r.Count())
	}

	err = pgtest.Identity{}.WithTx(t.Context(), func(ctx context.Context) error {
		return r.Publish(ctx, "note.created", "n1")
	})
	if err != nil {
		t.Fatalf("Publish in tx: %v", err)
	}
	if r.Last("note.created") != "n1" {
		t.Fatalf("Last(note.created) = %v, want n1", r.Last("note.created"))
	}
}
