package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
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

func TestStore_DropOldPartitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	// Заводим партицию 10 месяцев назад (вне диапазона миграции current..+3).
	old := time.Now().UTC().AddDate(0, -10, 0)
	oldName, oldSQL := pgstore.PartitionSQL("outbox", old)
	if _, err := db.Exec(ctx, oldSQL); err != nil {
		t.Fatalf("create old partition: %v", err)
	}

	if err := s.DropOldPartitions(ctx, 3); err != nil {
		t.Fatalf("drop old partitions: %v", err)
	}

	if partitionExists(t, ctx, db, oldName) {
		t.Fatalf("старая партиция %s не удалена", oldName)
	}
	curName, _ := pgstore.PartitionSQL("outbox", time.Now().UTC())
	if !partitionExists(t, ctx, db, curName) {
		t.Fatalf("текущая партиция %s удалена по ошибке", curName)
	}
}

// Партиция с неопубликованными событиями переживает retention; остальные старые дропаются.
func TestStore_DropOldPartitions_KeepsPending(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	pendingMonth := time.Now().UTC().AddDate(0, -10, 0)
	pendingName, pendingSQL := pgstore.PartitionSQL("outbox", pendingMonth)
	if _, err := db.Exec(ctx, pendingSQL); err != nil {
		t.Fatalf("create pending partition: %v", err)
	}
	emptyMonth := time.Now().UTC().AddDate(0, -9, 0)
	emptyName, emptySQL := pgstore.PartitionSQL("outbox", emptyMonth)
	if _, err := db.Exec(ctx, emptySQL); err != nil {
		t.Fatalf("create empty partition: %v", err)
	}
	deadMonth := time.Now().UTC().AddDate(0, -8, 0)
	deadName, deadSQL := pgstore.PartitionSQL("outbox", deadMonth)
	if _, err := db.Exec(ctx, deadSQL); err != nil {
		t.Fatalf("create dead partition: %v", err)
	}
	inMonth := time.Date(pendingMonth.Year(), pendingMonth.Month(), 15, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `INSERT INTO outbox (subject, payload, created_at) VALUES ('evt', '{}', $1)`, inMonth); err != nil {
		t.Fatalf("insert pending: %v", err)
	}
	deadAt := time.Date(deadMonth.Year(), deadMonth.Month(), 15, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `INSERT INTO outbox (subject, payload, created_at, dead_at) VALUES ('evt', '{}', $1, $1)`, deadAt); err != nil {
		t.Fatalf("insert dead: %v", err)
	}

	err := s.DropOldPartitions(ctx, 3)
	if !errors.Is(err, pgstore.ErrPendingRows) {
		t.Fatalf("err = %v, want ErrPendingRows", err)
	}
	if !partitionExists(t, ctx, db, pendingName) {
		t.Fatalf("партиция с неопубликованными %s удалена", pendingName)
	}
	if partitionExists(t, ctx, db, emptyName) {
		t.Fatalf("пустая старая партиция %s не удалена", emptyName)
	}
	if partitionExists(t, ctx, db, deadName) {
		t.Fatalf("партиция только с dead-строками %s не удалена", deadName)
	}
}

func partitionExists(t *testing.T, ctx context.Context, db *pgkit.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("partition exists check: %v", err)
	}
	return exists
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
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)

	for i := 0; i < 5; i++ {
		if err := s.Insert(ctx, "evt.test", map[string]int{"i": i}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	msgs := list(t, tr, s, ctx, 10)
	if len(msgs) != 5 {
		t.Fatalf("want 5 unpublished, got %d", len(msgs))
	}
	for _, m := range msgs {
		if err := s.MarkPublished(ctx, []int64{m.ID}); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
	}
	if left := list(t, tr, s, ctx, 10); len(left) != 0 {
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
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	if err := s.EnsurePartitions(ctx, 6); err != nil {
		t.Fatalf("EnsurePartitions idempotent: %v", err)
	}
}
