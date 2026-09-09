package testkit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

var errRollback = errors.New("rollback")

func kitSource(t *testing.T) pgkit.Source {
	t.Helper()
	return pgkit.Source{
		Name: "kit",
		FS: fstest.MapFS{
			"migrations/0001_kit.up.sql":   {Data: []byte(`CREATE TABLE kit_items (id INT PRIMARY KEY);`)},
			"migrations/0001_kit.down.sql": {Data: []byte(`DROP TABLE kit_items;`)},
		},
		Dir: "migrations",
	}
}

// appMigrationsDir кладёт миграцию, зависящую от таблицы кита: она применится
// только если источники идут в порядке передачи опций.
func appMigrationsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"0001_app.up.sql":   `CREATE TABLE app_items (id INT PRIMARY KEY REFERENCES kit_items(id));`,
		"0001_app.down.sql": `DROP TABLE app_items;`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func tableExists(t *testing.T, db *pgkit.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(t.Context(),
		`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, name).Scan(&n)
	if err != nil {
		t.Fatalf("query table %s: %v", name, err)
	}
	return n == 1
}

func TestStartPostgres_AppliesMigrationsInOptionOrder(t *testing.T) {
	fix := testkit.StartPostgres(t,
		testkit.WithMigrations(kitSource(t)),
		testkit.WithMigrationsDir(appMigrationsDir(t)),
	)
	db := fix.DB(t)

	for _, table := range []string{
		"kit_items", "app_items",
		"schema_migrations_kit", "schema_migrations_" + pgkit.AppSourceName,
	} {
		if !tableExists(t, db, table) {
			t.Errorf("table %s is missing after StartPostgres", table)
		}
	}
}

func TestStartPostgres_NoMigrationsByDefault(t *testing.T) {
	fix := testkit.StartPostgres(t)
	if fix.DSN == "" {
		t.Fatal("DSN is empty")
	}
	if fix.Container == nil {
		t.Fatal("Container is nil")
	}
	if tableExists(t, fix.DB(t), "schema_migrations_"+pgkit.AppSourceName) {
		t.Error("version table created without WithMigrations")
	}
}

// Pool/DB/Transactor обязаны делить один пул — иначе потребитель открывает второй.
func TestPostgresFixture_SharesOnePool(t *testing.T) {
	fix := testkit.StartPostgres(t)

	pool := fix.Pool(t)
	if pool != fix.Pool(t) {
		t.Fatal("Pool returned a second pool")
	}
	t.Run("subtest", func(t *testing.T) {
		if fix.Pool(t) != pool {
			t.Fatal("subtest got a different pool")
		}
	})

	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := fix.DB(t).Exec(t.Context(), `CREATE TABLE shared (id INT)`); err != nil {
		t.Fatalf("exec via DB: %v", err)
	}

	// Транзактор поверх того же пула: откат обязан убрать вставку.
	tx := fix.Transactor(t)
	if err := tx.WithTx(t.Context(), func(ctx context.Context) error {
		if _, err := fix.DB(t).Exec(ctx, `INSERT INTO shared VALUES (1)`); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("WithTx err = %v, want sentinel", err)
	}
	var n int
	if err := fix.DB(t).QueryRow(t.Context(), `SELECT count(*) FROM shared`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}
}

func TestPostgresFixture_PoolConfigHasProdTimeouts(t *testing.T) {
	fix := testkit.StartPostgres(t)
	cfg := fix.PoolConfig()
	if cfg.DSN != fix.DSN {
		t.Errorf("PoolConfig().DSN = %q, want fixture DSN", cfg.DSN)
	}
	if cfg.StatementTimeout == 0 || cfg.LockTimeout == 0 || cfg.IdleInTxSessionTimeout == 0 {
		t.Errorf("session timeouts must be set: %+v", cfg)
	}
	if !cfg.OTelTrace {
		t.Error("OTelTrace must be on, as in prod wiring")
	}

	var got string
	if err := fix.DB(t).QueryRow(t.Context(), `SHOW statement_timeout`).Scan(&got); err != nil {
		t.Fatalf("show statement_timeout: %v", err)
	}
	if got == "0" {
		t.Error("statement_timeout is not applied to fixture connections")
	}
}

func TestStartPostgres_WithImage(t *testing.T) {
	fix := testkit.StartPostgres(t, testkit.WithImage("postgres:16-alpine"))
	var version string
	if err := fix.DB(t).QueryRow(t.Context(), `SHOW server_version`).Scan(&version); err != nil {
		t.Fatalf("show server_version: %v", err)
	}
	if !strings.HasPrefix(version, "16") {
		t.Fatalf("server_version = %q, want 16.x from WithImage", version)
	}
}
