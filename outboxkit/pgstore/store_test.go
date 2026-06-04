package pgstore_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sftbmb.ru/sftbmb-core/golibinfra/outboxkit/pgstore"
	"git.sftbmb.ru/sftbmb-core/golibinfra/pgkit"
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

type sample struct {
	Foo string `json:"foo"`
	N   int    `json:"n"`
}

func TestStore_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	s := pgstore.NewStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.Insert(ctx, "user.created", sample{Foo: "bar", N: 7}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	msgs, err := s.ListUnpublished(ctx, 100)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Subject != "user.created" {
		t.Fatalf("subject mismatch: %s", msgs[0].Subject)
	}

	if err := s.MarkPublished(ctx, msgs[0].ID); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	msgs2, err := s.ListUnpublished(ctx, 100)
	if err != nil {
		t.Fatalf("ListUnpublished after publish: %v", err)
	}
	if len(msgs2) != 0 {
		t.Fatalf("want 0 unpublished after MarkPublished, got %d", len(msgs2))
	}
}

func TestStore_RollbackDropsMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	migDir := extractMigrations(t)
	f := testkit.StartPostgres(t, migDir)

	pool, err := pgxpool.New(context.Background(), f.DSN)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	db := pgkit.NewDB(pool)
	tr := pgkit.NewPoolTransactor(pool)
	s := pgstore.NewStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wantErr := errors.New("business failure")
	err = tr.WithTx(ctx, func(ctx context.Context) error {
		if err := s.Insert(ctx, "user.created", sample{Foo: "rollback", N: 1}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx err: got %v, want %v", err, wantErr)
	}

	msgs, err := s.ListUnpublished(ctx, 100)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("rollback failed — outbox kept message; got %d msgs", len(msgs))
	}

	err = tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "user.created", sample{Foo: "commit", N: 2})
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	msgs2, err := s.ListUnpublished(ctx, 100)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(msgs2) != 1 {
		t.Fatalf("want 1 msg after commit, got %d", len(msgs2))
	}
}
