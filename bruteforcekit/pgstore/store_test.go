package pgstore_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.sftbmb.ru/sftbmb-core/golibinfra/bruteforcekit"
	"git.sftbmb.ru/sftbmb-core/golibinfra/bruteforcekit/pgstore"
	"git.sftbmb.ru/sftbmb-core/golibinfra/testkit"
)

func extractMigrations(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(pgstore.MigrationsFS, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := pgstore.MigrationsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(path)), data, 0o644)
	})
	if err != nil {
		t.Fatalf("extract migrations: %v", err)
	}
	return dir
}

func TestStore_LockAfterMaxFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	cfg := bruteforcekit.Config{
		MaxFailures:  3,
		LockDuration: 15 * time.Minute,
		Window:       time.Hour,
	}
	s := pgstore.NewStore(db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

func TestStore_Cleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	cfg := bruteforcekit.Config{
		MaxFailures:      3,
		LockDuration:     15 * time.Minute,
		Window:           time.Hour,
		CleanupRetention: 0,
	}
	s := pgstore.NewStore(db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	cfg := bruteforcekit.Config{
		MaxFailures:  3,
		LockDuration: 15 * time.Minute,
		Window:       time.Hour,
	}
	s := pgstore.NewStore(db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
