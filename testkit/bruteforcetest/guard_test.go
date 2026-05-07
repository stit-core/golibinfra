package bruteforcetest_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/akadmin/golibinfra/bruteforcekit"
	"git.stit.tech/akadmin/golibinfra/testkit/bruteforcetest"
)

var (
	_ bruteforcekit.Guard = bruteforcetest.Permissive{}
	_ bruteforcekit.Guard = bruteforcetest.Locked{}
	_ bruteforcekit.Guard = (*bruteforcetest.Recorder)(nil)
)

func TestPermissive_NoLock(t *testing.T) {
	t.Parallel()
	st, err := bruteforcetest.Permissive{}.Check(context.Background(), "u1", "1.1.1.1")
	if err != nil || st != nil {
		t.Fatalf("expected nil/nil, got %v/%v", st, err)
	}
}

func TestLocked_ReturnsState(t *testing.T) {
	t.Parallel()
	until := time.Now().Add(15 * time.Minute)
	st, err := bruteforcetest.Locked{Until: until}.Check(context.Background(), "u1", "1.1.1.1")
	if err != nil || st == nil || !st.Until.Equal(until) {
		t.Fatalf("got %v %v", st, err)
	}
}

func TestRecorder_TracksRecords(t *testing.T) {
	t.Parallel()
	r := &bruteforcetest.Recorder{}
	ctx := context.Background()
	_ = r.RecordFailure(ctx, "u1", "ip")
	_ = r.RecordFailure(ctx, "u2", "ip")
	_ = r.RecordSuccess(ctx, "u1", "ip")
	if got := r.Failures(); len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("Failures=%v", got)
	}
	if got := r.Successes(); len(got) != 1 || got[0] != "u1" {
		t.Fatalf("Successes=%v", got)
	}
}
