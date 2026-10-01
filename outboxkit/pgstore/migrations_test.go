package pgstore_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// Откат кита — рабочий путь: down каждой версии возвращает схему к состоянию до её up —
// и у таблицы DefaultTable, и у таблицы инстанса.
func TestMigrations_Ladder(t *testing.T) {
	testkit.MigrationLadder(t, testkit.WithMigrations(pgstore.Migrations, pgstore.TableMigrations("outbox_orders")))
}
