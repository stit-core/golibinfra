package bruteforcekit_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.stit.tech/akadmin/golibinfra/bruteforcekit"
	"git.stit.tech/akadmin/golibinfra/testkit"
)

func extractMigrations(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(bruteforcekit.Migrations, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := bruteforcekit.Migrations.ReadFile(path)
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

func TestPgGuard_LockAfterMaxFailures(t *testing.T) {
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
	g := bruteforcekit.NewPgGuard(db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const subject = "user@example.com"
	const ip = "10.0.0.1"

	if st, err := g.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("initial Check: st=%v err=%v", st, err)
	}

	for i := 0; i < 2; i++ {
		if err := g.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	if st, err := g.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("after 2 failures Check: st=%v err=%v", st, err)
	}

	before := time.Now()
	if err := g.RecordFailure(ctx, subject, ip); err != nil {
		t.Fatalf("RecordFailure threshold: %v", err)
	}
	st, err := g.Check(ctx, subject, ip)
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

func TestPgGuard_RecordSuccessResets(t *testing.T) {
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
	g := bruteforcekit.NewPgGuard(db, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const subject = "phone:+15555550123"
	const ip = "10.0.0.2"

	for i := 0; i < 2; i++ {
		if err := g.RecordFailure(ctx, subject, ip); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	if err := g.RecordSuccess(ctx, subject, ip); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	if st, err := g.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("Check after success: st=%v err=%v", st, err)
	}
	if err := g.RecordFailure(ctx, subject, ip); err != nil {
		t.Fatalf("RecordFailure after reset: %v", err)
	}
	if st, err := g.Check(ctx, subject, ip); err != nil || st != nil {
		t.Fatalf("Check after single failure (reset): st=%v err=%v", st, err)
	}
}
