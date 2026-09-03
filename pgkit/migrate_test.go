package pgkit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func tableExists(t *testing.T, db *pgkit.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query %s: %v", name, err)
	}
	return n == 1
}

func version(t *testing.T, dsn string, src pgkit.Source) uint {
	t.Helper()
	v, dirty, err := pgkit.MigrateVersion(context.Background(), dsn, src)
	if err != nil || dirty {
		t.Fatalf("MigrateVersion %s: v=%d dirty=%v err=%v", src.Name, v, dirty, err)
	}
	return v
}

// Новая миграция в ките применяется одна и не трогает версии приложения.
func TestMigrateUpAll_PerSourceVersions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	app := fstest.MapFS{
		"0001_users.up.sql":   {Data: []byte(`CREATE TABLE users (id INT PRIMARY KEY);`)},
		"0001_users.down.sql": {Data: []byte(`DROP TABLE users;`)},
		"0002_items.up.sql":   {Data: []byte(`CREATE TABLE items (id INT PRIMARY KEY);`)},
		"0002_items.down.sql": {Data: []byte(`DROP TABLE items;`)},
	}
	kit := fstest.MapFS{
		"migrations/0001_a.up.sql":   {Data: []byte(`CREATE TABLE a_log (v INT);`)},
		"migrations/0001_a.down.sql": {Data: []byte(`DROP TABLE a_log;`)},
	}
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	appSrc := pgkit.Source{Name: "app", FS: app, Dir: "."}
	kitSrc := pgkit.Source{Name: "a", FS: kit, Dir: "migrations"}
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, kitSrc, appSrc); err != nil {
		t.Fatalf("MigrateUpAll: %v", err)
	}
	for _, tbl := range []string{"users", "items", "a_log", "schema_migrations_app", "schema_migrations_a"} {
		if !tableExists(t, db, tbl) {
			t.Fatalf("table %s missing", tbl)
		}
	}
	if v := version(t, pg.DSN, appSrc); v != 2 {
		t.Fatalf("app version = %d, want 2", v)
	}
	if v := version(t, pg.DSN, kitSrc); v != 1 {
		t.Fatalf("kit version = %d, want 1", v)
	}

	kit["migrations/0002_a_more.up.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO a_log VALUES (2);`)}
	kit["migrations/0002_a_more.down.sql"] = &fstest.MapFile{Data: []byte(`DELETE FROM a_log WHERE v = 2;`)}
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, kitSrc, appSrc); err != nil {
		t.Fatalf("MigrateUpAll second run: %v", err)
	}
	if v := version(t, pg.DSN, appSrc); v != 2 {
		t.Fatalf("app version after kit update = %d, want 2 (untouched)", v)
	}
	if v := version(t, pg.DSN, kitSrc); v != 2 {
		t.Fatalf("kit version = %d, want 2", v)
	}
	var applied int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM a_log WHERE v = 2`).Scan(&applied); err != nil {
		t.Fatalf("count a_log: %v", err)
	}
	if applied != 1 {
		t.Fatalf("kit v2 applied %d times, want exactly 1", applied)
	}

	if err := pgkit.MigrateDown(ctx, pg.DSN, kitSrc); err != nil {
		t.Fatalf("MigrateDown: %v", err)
	}
	if v := version(t, pg.DSN, kitSrc); v != 1 {
		t.Fatalf("kit version after down = %d, want 1", v)
	}
	if v := version(t, pg.DSN, appSrc); v != 2 {
		t.Fatalf("app version after kit down = %d, want 2", v)
	}
}

func TestMigrateModule_KitsThenAppDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0001_orders.up.sql"),
		[]byte(`CREATE TABLE orders (id INT PRIMARY KEY, audit_ref INT REFERENCES b_log(v));`), 0o600); err != nil {
		t.Fatal(err)
	}
	kit := fstest.MapFS{
		"migrations/0001_b.up.sql": {Data: []byte(`CREATE TABLE b_log (v INT PRIMARY KEY);`)},
	}
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			pgkit.PoolConfig{DSN: pg.DSN},
			pgkit.MigrateRunnerConfig{Path: dir},
		),
		pgkit.ProvideMigrations(pgkit.Source{Name: "b", FS: kit, Dir: "migrations"}),
		pgkit.MigrateModule,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, tbl := range []string{"b_log", "orders", "schema_migrations_b", "schema_migrations_app"} {
		if !tableExists(t, db, tbl) {
			t.Fatalf("table %s missing", tbl)
		}
	}
}

func TestMigrateModule_EmptyPathSkipsApp(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	kit := fstest.MapFS{
		"migrations/0001_c.up.sql": {Data: []byte(`CREATE TABLE c_log (v INT);`)},
	}
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(pgkit.PoolConfig{DSN: pg.DSN}, pgkit.MigrateRunnerConfig{Path: ""}),
		pgkit.ProvideMigrations(pgkit.Source{Name: "c", FS: kit, Dir: "migrations"}),
		pgkit.MigrateModule,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = app.Stop(ctx)
	if !tableExists(t, db, "c_log") {
		t.Fatal("kit table missing")
	}
	if tableExists(t, db, "schema_migrations_app") {
		t.Fatal("app source applied despite empty PG_MIGRATIONS_PATH")
	}
}

func TestMigrateUp_Validation(t *testing.T) {
	ctx := context.Background()
	fsys := fstest.MapFS{"0001_x.up.sql": {Data: []byte(`SELECT 1;`)}}

	for _, name := range []string{"", "App", "kit-a", "a b", "1a"} {
		if err := pgkit.MigrateUp(ctx, "postgres://x", pgkit.Source{Name: name, FS: fsys, Dir: "."}); err == nil {
			t.Fatalf("source name %q accepted", name)
		}
	}
	if err := pgkit.MigrateUp(ctx, "postgres://x", pgkit.Source{Name: "nofs"}); err == nil {
		t.Fatal("nil FS accepted")
	}
	if err := pgkit.MigrateUp(ctx, "postgres://x", pgkit.Source{Name: "empty", FS: fstest.MapFS{}, Dir: "."}); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := pgkit.MigrateUpAll(ctx, "postgres://x",
		pgkit.Source{Name: "dup", FS: fsys, Dir: "."},
		pgkit.Source{Name: "dup", FS: fsys, Dir: "."},
	); err == nil {
		t.Fatal("duplicate source names accepted")
	}
	if err := pgkit.MigrateUpAll(ctx, "postgres://x"); err != nil {
		t.Fatalf("no sources must be a no-op, got %v", err)
	}
}
