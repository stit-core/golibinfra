package pgstore_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func startStore(t *testing.T, cfg bruteforcekit.Config) (*pgstore.Store, *pgkit.DB, context.Context) {
	t.Helper()
	fx := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	db := fx.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return pgstore.NewStore(db, cfg), db, ctx
}

func TestConfig_Validate(t *testing.T) {
	ok := bruteforcekit.Config{MaxFailures: 1, LockDuration: time.Second}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, bad := range []bruteforcekit.Config{
		{MaxFailures: 0, LockDuration: time.Second},
		{MaxFailures: 3, LockDuration: 0},
		{MaxFailures: 3, LockDuration: time.Second, CleanupRetention: -time.Second},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", bad)
		}
	}
}

func TestStore_LockAfterMaxFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	cfg := bruteforcekit.Config{MaxFailures: 3, LockDuration: 15 * time.Minute}
	s, _, ctx := startStore(t, cfg)

	const subject = "user@example.com"
	const ip = "10.0.0.1"

	if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("initial Check: st=%v err=%v", st, err)
	}

	for i := 0; i < 2; i++ {
		if err := s.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("after 2 failures Check: st=%v err=%v", st, err)
	}

	before := time.Now()
	if err := s.RecordFailure(ctx, subject, ip); err != nil {
		t.Fatalf("RecordFailure threshold: %v", err)
	}
	st, err := s.Check(ctx, subject, ip)
	if err != nil {
		t.Fatalf("Check after lock: %v", err)
	}
	if st == nil {
		t.Fatalf("expected lock state, got nil")
	}
	minUntil := before.Add(cfg.LockDuration - time.Minute)
	maxUntil := time.Now().Add(cfg.LockDuration + time.Minute)
	if st.Until.Before(minUntil) || st.Until.After(maxUntil) {
		t.Fatalf("Until out of range: got %v, want in [%v, %v]", st.Until, minUntil, maxUntil)
	}
}

// MaxFailures=1: блокировка с первой же неудачи (INSERT-ветка upsert'а).
func TestStore_LockOnFirstFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	s, _, ctx := startStore(t, bruteforcekit.Config{MaxFailures: 1, LockDuration: 90 * time.Second})

	if err := s.RecordFailure(ctx, "one@example.com", "10.0.0.9"); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	st, err := s.Check(ctx, "one@example.com", "10.0.0.9")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st == nil {
		t.Fatal("expected lock after first failure with MaxFailures=1")
	}
	if remaining := time.Until(st.Until); remaining < 60*time.Second || remaining > 2*time.Minute {
		t.Fatalf("Until = %v (remaining %v), want ~90s", st.Until, remaining)
	}
}

// Check решает по часам базы: истёкший locked_until — не блокировка.
func TestStore_CheckUsesDatabaseClock(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	s, db, ctx := startStore(t, bruteforcekit.Config{MaxFailures: 1, LockDuration: time.Second})

	const subject, ip = "clock@example.com", "10.0.0.3"
	if _, err := db.Exec(ctx, `INSERT INTO bruteforce_attempts (subject, ip, failed_count, locked_until)
		VALUES ($1, $2, 1, NOW() - interval '1 second')`, subject, ip); err != nil {
		t.Fatalf("insert expired: %v", err)
	}
	if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("expired lock reported: st=%v err=%v", st, err)
	}
	if _, err := db.Exec(ctx, `UPDATE bruteforce_attempts SET locked_until = NOW() + interval '1 hour'
		WHERE subject = $1 AND ip = $2`, subject, ip); err != nil {
		t.Fatalf("update future: %v", err)
	}
	if st, err := s.Check(ctx, subject, ip); err != nil || st == nil {
		t.Fatalf("active lock missed: st=%v err=%v", st, err)
	}
}

func TestStore_Cleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	s, _, ctx := startStore(t, bruteforcekit.Config{MaxFailures: 3, LockDuration: 15 * time.Minute, CleanupRetention: 0})

	if err := s.RecordFailure(ctx, "stale@example.com", "1.1.1.1"); err != nil {
		t.Fatalf("RecordFailure stale: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordFailure(ctx, "locked@example.com", "2.2.2.2"); err != nil {
			t.Fatalf("RecordFailure locked %d: %v", i, err)
		}
	}

	if err := s.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if st, err := s.Check(ctx, "stale@example.com", "1.1.1.1"); err != nil || st != nil {
		t.Fatalf("stale row should be gone: st=%v err=%v", st, err)
	}
	st, err := s.Check(ctx, "locked@example.com", "2.2.2.2")
	if err != nil {
		t.Fatalf("Check locked: %v", err)
	}
	if st == nil {
		t.Fatalf("locked row must survive Cleanup")
	}
}

func TestStore_RecordSuccessResets(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	s, _, ctx := startStore(t, bruteforcekit.Config{MaxFailures: 3, LockDuration: 15 * time.Minute})

	const subject = "phone:+15555550123"
	const ip = "10.0.0.2"

	for i := 0; i < 2; i++ {
		if err := s.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	if err := s.RecordSuccess(ctx, subject, ip); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("Check after success: st=%v err=%v", st, err)
	}
	if err := s.RecordFailure(ctx, subject, ip); err != nil {
		t.Fatalf("RecordFailure after reset: %v", err)
	}
	if st, err := s.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("Check after single failure (reset): st=%v err=%v", st, err)
	}
}
