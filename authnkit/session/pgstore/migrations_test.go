package pgstore_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// Откат кита — рабочий путь: down каждой версии возвращает схему к состоянию до её up.
func TestMigrations_Ladder(t *testing.T) {
	testkit.MigrationLadder(t, testkit.WithMigrations(pgstore.Migrations))
}
