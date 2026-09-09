package outboxtest_test

import (
	"context"
	"testing"

	"git.stit.tech/stit-core/golibinfra/testkit/outboxtest"
)

type narrowBus interface {
	Publish(ctx context.Context, subject string, payload any) error
}

var _ narrowBus = (*outboxtest.Recorder)(nil)

func TestRecorder_PublishAndRead(t *testing.T) {
	t.Parallel()
	r := &outboxtest.Recorder{}
	ctx := context.Background()
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
	if err := r.Publish(t.Context(), "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Publish(t.Context(), "b", 2); err != nil {
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
