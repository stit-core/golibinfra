package stopbudget

import (
	"context"
	"testing"
	"time"
)

func TestShare_LeavesAThirdToLaterHooks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	share, stop := Share(ctx)
	defer stop()
	deadline, ok := share.Deadline()
	if left := time.Until(deadline); !ok || left > 2*time.Second || left < 1900*time.Millisecond {
		t.Fatalf("share of a 3s budget: deadline in %s (set=%v), want two thirds — 2s", left, ok)
	}
}

func TestShare_NoDeadlineFollowsParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	share, stop := Share(parent)
	defer stop()
	if _, ok := share.Deadline(); ok {
		t.Fatal("a stop ctx without a deadline must not get one")
	}
	cancel()
	select {
	case <-share.Done():
	case <-time.After(time.Second):
		t.Fatal("the share must be cancelled with its parent")
	}
}
