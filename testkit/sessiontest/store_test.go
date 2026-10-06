package sessiontest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/testkit/sessiontest"
)

// Фейк держит тот же контракт, что pgstore: юнит-тест сценария на нём не
// расходится с продом.
func TestStore_Contract(t *testing.T) {
	sessiontest.RunContract(t, func(t *testing.T, cfg session.Config, clock clockkit.Clock) session.Store {
		s, err := sessiontest.New(cfg, clock)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	})
}

func TestNew_ValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := sessiontest.New(session.Config{}, nil); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("New(zero config) = %v, want ErrConfig", err)
	}
}

func TestStore_Active(t *testing.T) {
	t.Parallel()
	clock := clockkit.NewTickable(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := sessiontest.New(session.Config{IdleTTL: time.Hour, AbsoluteTTL: 2 * time.Hour, CleanupInterval: time.Hour}, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	for range 2 {
		if _, err := s.Issue(ctx, "u1"); err != nil {
			t.Fatalf("Issue: %v", err)
		}
	}
	if _, err := s.Issue(ctx, "u2"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got := s.Active("u1"); got != 2 {
		t.Fatalf("Active(u1) = %d, want 2", got)
	}
	if err := s.RevokeAll(ctx, "u1"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if got := s.Active("u1"); got != 0 {
		t.Fatalf("Active(u1) after RevokeAll = %d, want 0", got)
	}
	clock.Advance(2 * time.Hour)
	if got := s.Active("u2"); got != 0 {
		t.Fatalf("Active(u2) after the absolute deadline = %d, want 0", got)
	}
}
