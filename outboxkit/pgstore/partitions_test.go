package pgstore_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestPartitionSQL_Naming(t *testing.T) {
	name, sql := pgstore.PartitionSQL("outbox_orders", time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if name != "outbox_orders_2026_05" {
		t.Fatalf("name: %s", name)
	}
	if sql == "" {
		t.Fatalf("empty sql")
	}
}

func TestSchemaSQL_NotEmpty(t *testing.T) {
	if pgstore.SchemaSQL("outbox") == "" {
		t.Fatal("schema sql is empty")
	}
}

func TestNewStoreFor_Validation(t *testing.T) {
	if _, err := pgstore.NewStoreFor(nil, "outbox_orders"); err != nil {
		t.Fatalf("valid table rejected: %v", err)
	}
	for _, bad := range []string{"", "Orders", "outbox orders", "1outbox", "outbox;drop"} {
		if _, err := pgstore.NewStoreFor(nil, bad); err == nil {
			t.Fatalf("invalid table %q accepted", bad)
		}
	}
}

func TestStore_PartitionedDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := pgstore.NewStore(db)

	for i := 0; i < 5; i++ {
		if err := s.Insert(ctx, "evt.test", map[string]int{"i": i}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	msgs, err := s.ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want 5 unpublished, got %d", len(msgs))
	}
	for _, m := range msgs {
		if err := s.MarkPublished(ctx, []int64{m.ID}); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
	}
	left, err := s.ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished after: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("want 0, got %d", len(left))
	}

	row := db.QueryRow(ctx, "SELECT relkind::text FROM pg_class WHERE relname='outbox'")
	var kind string
	if err := row.Scan(&kind); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if kind != "p" {
		t.Fatalf("expected partitioned table (relkind=p), got %s", kind)
	}
}

func TestStore_EnsurePartitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)
	s := pgstore.NewStore(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions idempotent: %v", err)
	}
}
