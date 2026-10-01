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

	for name, fix := range map[string]string{
		"dirty":  `UPDATE schema_migrations_authnkit_session SET dirty = true`,
		"behind": `UPDATE schema_migrations_authnkit_session SET version = 0, dirty = false`,
		"empty":  `DELETE FROM schema_migrations_authnkit_session`,
	} {
		if _, err := db.Exec(ctx, fix); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := s.CheckSchema(ctx); !errors.Is(err, pgstore.ErrSchema) {
			t.Errorf("%s version table: CheckSchema = %v, want ErrSchema", name, err)
		}
	}
}
