package pgstore_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
)

// Партиции текущего и следующего месяца существуют сразу после старта Module,
// не дожидаясь суточного PartitionKeeperJob.
func TestModule_EnsuresPartitionsOnStart(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}
	db, _, ctx := startStore(t)

	now := time.Now().UTC()
	curName, _ := pgstore.PartitionSQL(pgstore.DefaultTable, now)
	nextName, _ := pgstore.PartitionSQL(pgstore.DefaultTable, now.AddDate(0, 1, 0))
	for _, name := range []string{curName, nextName} {
		if _, err := db.Exec(ctx, "DROP TABLE "+name); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	if partitionExists(t, ctx, db, curName) {
		t.Fatal("precondition: current partition must be absent")
	}

	app := fx.New(
		fx.NopLogger,
		fx.Supply(db, pgstore.Config{}),
		pgstore.Module,
	)
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for _, name := range []string{curName, nextName} {
		if !partitionExists(t, ctx, db, name) {
			t.Fatalf("partition %s missing after Module start", name)
		}
	}
}
