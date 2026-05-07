package outboxkit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/akadmin/golibinfra/outboxkit"
	"git.stit.tech/akadmin/golibinfra/testkit"
)

func TestPartitionSQL_Naming(t *testing.T) {
	name, sql := outboxkit.PartitionSQL(time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	if name != "outbox_2026_05" {
		t.Fatalf("name: %s", name)
	}
	if sql == "" {
		t.Fatalf("empty sql")
	}
}

func TestSchemaSQL_NotEmpty(t *testing.T) {
	if outboxkit.SchemaSQL() == "" {
		t.Fatal("schema sql is empty")
	}
}

func TestRepository_PartitionedDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	migDir := extractMigrations(t)
	fx := testkit.StartPostgres(t, migDir)
	db := fx.DB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := outboxkit.NewRepository(db)

	for i := 0; i < 5; i++ {
		if err := repo.Insert(ctx, "evt.test", map[string]int{"i": i}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	msgs, err := repo.ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want 5 unpublished, got %d", len(msgs))
	}
	for _, m := range msgs {
		if err := repo.MarkPublished(ctx, m.ID); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
	}
	left, err := repo.ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished after: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("want 0, got %d", len(left))
	}

	row := db.QueryRow(ctx, "SELECT relkind FROM pg_class WHERE relname='outbox'")
	var kind string
	if err := row.Scan(&kind); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if kind != "p" {
		t.Fatalf("expected partitioned table (relkind=p), got %s", kind)
	}
}
