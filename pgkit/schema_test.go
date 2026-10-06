package pgkit_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func schemaSource() pgkit.Source {
	return pgkit.Source{Name: "kitschema", FS: fstest.MapFS{
		"m/0001_a.up.sql":   {Data: []byte(`CREATE TABLE kitschema_a (v INT);`)},
		"m/0001_a.down.sql": {Data: []byte(`DROP TABLE kitschema_a;`)},
		"m/0002_b.up.sql":   {Data: []byte(`CREATE TABLE kitschema_b (v INT);`)},
		"m/0002_b.down.sql": {Data: []byte(`DROP TABLE kitschema_b;`)},
	}, Dir: "m"}
}

// Хранилище кита без своих миграций падает на старте, а не на первой записи: CheckSchema
// называет источник и как его применить, на каждом виде поломки таблицы версий.
func TestCheckSchema(t *testing.T) {
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	src := schemaSource()
	ctx := t.Context()

	err := pgkit.CheckSchema(ctx, db, src)
	if !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), `source "kitschema": schema_migrations_kitschema is missing`) ||
		!strings.Contains(err.Error(), "pgkit.MigrateModule") {
		t.Fatalf("CheckSchema without migrations = %v, want ErrSchema naming the source with a hint", err)
	}
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, time.Minute, src); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := pgkit.CheckSchema(ctx, db, src); err != nil {
		t.Fatalf("CheckSchema after migrations: %v", err)
	}

	// Срез, а не map: поломки копятся, и dirty проверяется на последней версии, до остальных.
	for _, c := range []struct{ name, fix, want string }{
		{"dirty", `UPDATE schema_migrations_kitschema SET dirty = true`, "version 2 is dirty"},
		{"behind", `UPDATE schema_migrations_kitschema SET version = 1, dirty = false`, "version 1, the source needs 2"},
		{"empty", `DELETE FROM schema_migrations_kitschema`, "no migration applied"},
	} {
		if _, err := db.Exec(ctx, c.fix); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if err := pgkit.CheckSchema(ctx, db, src); !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s version table: CheckSchema = %v, want ErrSchema with %q", c.name, err, c.want)
		}
	}
}

// CheckSchemaOnStart: без миграций старт падает, с pgkit.MigrateModule проверка идёт после
// миграций, в каком бы порядке ни стояли модули.
func TestCheckSchemaOnStart(t *testing.T) {
	pg := testkit.StartPostgres(t)
	src := schemaSource()
	app := func(extra ...fx.Option) *fx.App {
		return fx.New(append([]fx.Option{
			fx.NopLogger,
			// Модуль хранилища раньше MigrateModule: порядок держит pgkit.Migrated, а не позиция.
			fx.Module("kitstore", pgkit.CheckSchemaOnStart(src)),
			fx.Supply(pg.PoolConfig()),
			fx.Provide(logkit.Nop),
			pgkit.Module,
		}, extra...)...)
	}

	bare := app()
	if err := bare.Start(t.Context()); !errors.Is(err, pgkit.ErrSchema) {
		t.Fatalf("Start without migrations = %v, want ErrSchema", err)
	}
	_ = bare.Stop(context.Background())

	migrated := app(
		fx.Supply(pgkit.MigrateRunnerConfig{Timeout: time.Minute, LockTimeout: time.Minute}),
		pgkit.SupplyMigrations(src),
		pgkit.MigrateModule,
	)
	if err := migrated.Start(t.Context()); err != nil {
		t.Fatalf("Start with pgkit.MigrateModule: %v", err)
	}
	if err := migrated.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
