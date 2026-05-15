package audittest_test

import (
	"context"
	"testing"

	"git.stit.tech/stit-infra/golibinfra/auditkit"
	"git.stit.tech/stit-infra/golibinfra/testkit/audittest"
)

var _ auditkit.Logger = (*audittest.Recorder)(nil)

func TestRecorder_RoundTrip(t *testing.T) {
	t.Parallel()
	r := &audittest.Recorder{}
	ctx := context.Background()
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth", ActorID: "u1"})
	mustLog(t, r, auditkit.Entry{Action: "register", Resource: "user", ActorID: "u1"})
	mustLog(t, r, auditkit.Entry{Action: "login", Resource: "auth", ActorID: "u2"})

	if got := r.Actions(); len(got) != 3 || got[0] != "login" || got[2] != "login" {
		t.Fatalf("Actions=%v", got)
	}
	if r.Last("login") == nil || r.Last("login").ActorID != "u2" {
		t.Fatalf("Last(login)=%+v", r.Last("login"))
	}
	if r.Last("missing") != nil {
		t.Fatalf("Last(missing) must be nil")
	}
	if got := r.ByResource("user"); len(got) != 1 || got[0].Action != "register" {
		t.Fatalf("ByResource(user)=%+v", got)
	}
	r.Reset()
	if len(r.Entries()) != 0 {
		t.Fatalf("Reset did not clear entries")
	}
	_ = ctx
}

func mustLog(t *testing.T, r *audittest.Recorder, e auditkit.Entry) {
	t.Helper()
	if err := r.Log(context.Background(), e); err != nil {
		t.Fatalf("log: %v", err)
	}
}
