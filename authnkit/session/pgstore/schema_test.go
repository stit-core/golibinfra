package pgstore_test

import (
	"errors"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// Без миграций кита сервис не стартует: иначе первый вход упал бы на 42P01.
func TestStore_CheckSchema(t *testing.T) {
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	s := newStore(t, db, clockkit.Real{})
	ctx := t.Context()

	err := s.CheckSchema(ctx)
	if !errors.Is(err, pgstore.ErrSchema) || !strings.Contains(err.Error(), "pgstore.Migrations") {
		t.Fatalf("CheckSchema without migrations = %v, want ErrSchema with a hint", err)
	}
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, pgstore.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema after migrations: %v", err)
	}
	if pgstore.Migrations.VersionTable() != "schema_migrations_authnkit_session" {
		t.Fatalf("version table %s: CheckSchema reads schema_migrations_authnkit_session", pgstore.Migrations.VersionTable())
	}

	// Срез, а не map: поломки копятся, и dirty проверяется на последней версии, до остальных.
	for _, c := range []struct{ name, fix, want string }{
		{"dirty", `UPDATE schema_migrations_authnkit_session SET dirty = true`, "is dirty"},
		{"behind", `UPDATE schema_migrations_authnkit_session SET version = 0, dirty = false`, "the kit needs"},
		{"empty", `DELETE FROM schema_migrations_authnkit_session`, "no migration applied"},
	} {
		if _, err := db.Exec(ctx, c.fix); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := s.CheckSchema(ctx); !errors.Is(err, pgstore.ErrSchema) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s version table: CheckSchema = %v, want ErrSchema with %q", c.name, err, c.want)
		}
	}
}
