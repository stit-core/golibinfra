package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

type sample struct {
	Foo string `json:"foo"`
	N   int    `json:"n"`
}

// startStore поднимает Postgres с миграцией кита и отдаёт DB + Transactor на одном пуле.
func startStore(t *testing.T) (*pgkit.DB, *pgkit.PoolTransactor, context.Context) {
	t.Helper()
	fix := testkit.StartPostgres(t, testkit.WithMigrations(pgstore.Migrations))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, fix.DSN)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pgkit.NewDB(pool), pgkit.NewPoolTransactor(pool), ctx
}

// list читает батч в собственной транзакции (как это делает Worker).
func list(t *testing.T, tr pgkit.Transactor, s *pgstore.Store, ctx context.Context, limit int) []outboxkit.Message {
	t.Helper()
	var msgs []outboxkit.Message
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		var err error
		msgs, err = s.ListUnpublished(ctx, limit, outboxkit.PublishBatch)
		return err
	})
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	return msgs
}

func TestStore_RequireTx_NoTxErrors(t *testing.T) {
	s := pgstore.NewStore(nil, pgstore.WithRequireTx())
	if err := s.Insert(context.Background(), "evt", sample{N: 1}); err == nil {
		t.Fatal("expected error: Insert вне транзакции при WithRequireTx")
	}
}

func TestStore_RequireTx_WithinTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db, pgstore.WithRequireTx())

	// вне транзакции — отказ.
	if err := s.Insert(ctx, "evt", sample{N: 1}); err == nil {
		t.Fatal("expected error outside tx")
	}

	// внутри WithTx — успех, строка коммитится атомарно.
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "evt", sample{N: 2})
	}); err != nil {
		t.Fatalf("insert within tx: %v", err)
	}

	if msgs := list(t, tr, s, ctx, 10); len(msgs) != 1 {
		t.Fatalf("list len=%d (ожидалась только запись из tx)", len(msgs))
	}
}

func TestStore_PoisonAndBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	if err := s.Insert(ctx, "evt", sample{N: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Insert(ctx, "evt", sample{N: 2}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 2 {
		t.Fatalf("list len=%d", len(msgs))
	}

	// одно — dead, другое — транзиентный фейл.
	if err := s.RecordFailures(ctx, []outboxkit.Failure{
		{ID: msgs[0].ID, Err: "boom", Dead: true},
		{ID: msgs[1].ID, Err: "transient", Dead: false},
	}); err != nil {
		t.Fatalf("record failures: %v", err)
	}

	left := list(t, tr, s, ctx, 10)
	if len(left) != 1 || left[0].ID != msgs[1].ID {
		t.Fatalf("dead не исключён: %+v", left)
	}
	if left[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", left[0].Attempts)
	}

	backlog, err := s.Backlog(ctx)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if len(backlog) != 1 || backlog[0].Subject != "evt" || backlog[0].Pending != 1 {
		t.Fatalf("backlog = %+v, want 1 pending for evt", backlog)
	}
	if backlog[0].Oldest <= 0 {
		t.Fatalf("backlog oldest = %v, want > 0", backlog[0].Oldest)
	}
}

func TestStore_CustomTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)

	const table = "outbox_orders"
	if _, err := db.Exec(ctx, pgstore.SchemaSQL(table)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	s, err := pgstore.NewStoreFor(db, table)
	if err != nil {
		t.Fatalf("NewStoreFor: %v", err)
	}
	if err := s.EnsurePartitions(ctx, 1); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}

	if err := s.Insert(ctx, "order.created", sample{Foo: "x", N: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 1 || msgs[0].Subject != "order.created" {
		t.Fatalf("unexpected msgs: %+v", msgs)
	}
	if err := s.MarkPublished(ctx, []int64{msgs[0].ID}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
		t.Fatalf("want 0 unpublished, got %d", len(left))
	}
}

func TestStore_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	if err := s.Insert(ctx, "user.created", sample{Foo: "bar", N: 7}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	msgs := list(t, tr, s, ctx, 100)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].Subject != "user.created" {
		t.Fatalf("subject mismatch: %s", msgs[0].Subject)
	}

	if err := s.MarkPublished(ctx, []int64{msgs[0].ID}); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	var publishedAt *time.Time
	if err := db.QueryRow(ctx, `SELECT published_at FROM outbox WHERE id = $1`, msgs[0].ID).Scan(&publishedAt); err != nil {
		t.Fatalf("select published_at: %v", err)
	}
	if publishedAt == nil {
		t.Fatal("published_at not set")
	}

	if msgs2 := list(t, tr, s, ctx, 100); len(msgs2) != 0 {
		t.Fatalf("want 0 unpublished after MarkPublished, got %d", len(msgs2))
	}
}

func TestStore_RollbackDropsMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	wantErr := errors.New("business failure")
	err := tr.WithTx(ctx, func(ctx context.Context) error {
		if err := s.Insert(ctx, "user.created", sample{Foo: "rollback", N: 1}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx err: got %v, want %v", err, wantErr)
	}

	if msgs := list(t, tr, s, ctx, 100); len(msgs) != 0 {
		t.Fatalf("rollback failed — outbox kept message; got %d msgs", len(msgs))
	}

	err = tr.WithTx(ctx, func(ctx context.Context) error {
		return s.Insert(ctx, "user.created", sample{Foo: "commit", N: 2})
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}
	if msgs2 := list(t, tr, s, ctx, 100); len(msgs2) != 1 {
		t.Fatalf("want 1 msg after commit, got %d", len(msgs2))
	}
}

// Ordered: пока одна транзакция держит голову очереди, вторая получает ErrBusy,
// а не следующие строки.
func TestStore_ListOrdered_HeadLockedIsBusy(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	for i := 0; i < 4; i++ {
		if err := s.Insert(ctx, "evt", sample{N: i}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	holder := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = tr.WithTx(ctx, func(ctx context.Context) error {
			if _, err := s.ListUnpublished(ctx, 2, outboxkit.PublishOrdered); err != nil {
				return err
			}
			close(holder)
			<-release
			return nil
		})
	}()
	<-holder
	defer close(release)

	err := tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := s.ListUnpublished(ctx, 2, outboxkit.PublishOrdered)
		return err
	})
	if !errors.Is(err, outboxkit.ErrBusy) {
		t.Fatalf("ordered list with locked head: err=%v, want ErrBusy", err)
	}

	// batch-режим в той же ситуации берёт следующие строки.
	var got []outboxkit.Message
	err = tr.WithTx(ctx, func(ctx context.Context) error {
		var err error
		got, err = s.ListUnpublished(ctx, 2, outboxkit.PublishBatch)
		return err
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("batch list: err=%v len=%d, want 2 unlocked rows", err, len(got))
	}
}
