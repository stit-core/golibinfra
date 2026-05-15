package pgkit_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"git.stit.tech/stit-infra/golibinfra/pgkit"
	"git.stit.tech/stit-infra/golibinfra/testkit"
)

func TestMigrateUpMulti_TwoSources(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in -short")
	}

	src1 := fstest.MapFS{
		"migrations/0001_foo.up.sql":   {Data: []byte(`CREATE TABLE foo (id INT PRIMARY KEY);`)},
		"migrations/0001_foo.down.sql": {Data: []byte(`DROP TABLE foo;`)},
	}
	src2 := fstest.MapFS{
		"migrations/0001_bar.up.sql":   {Data: []byte(`CREATE TABLE bar (id INT PRIMARY KEY);`)},
		"migrations/0001_bar.down.sql": {Data: []byte(`DROP TABLE bar;`)},
		"migrations/0002_baz.up.sql":   {Data: []byte(`CREATE TABLE baz (id INT PRIMARY KEY);`)},
		"migrations/0002_baz.down.sql": {Data: []byte(`DROP TABLE baz;`)},
	}

	fx := testkit.StartPostgres(t, "")
	cfg := pgkit.MultiSourceConfig{
		DSN: fx.DSN,
		Sources: []pgkit.Source{
			{Name: "src1", FS: src1, Prefix: "migrations"},
			{Name: "src2", FS: src2, Prefix: "migrations"},
		},
	}

	if err := pgkit.MigrateUpMulti(cfg); err != nil {
		t.Fatalf("MigrateUpMulti: %v", err)
	}

	db := fx.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, tbl := range []string{"foo", "bar", "baz"} {
		var n int
		err := db.QueryRow(
			ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, tbl,
		).Scan(&n)
		if err != nil {
			t.Fatalf("query %s: %v", tbl, err)
		}
		if n != 1 {
			t.Fatalf("table %s missing (count=%d)", tbl, n)
		}
	}

	v, dirty, err := pgkit.MigrateVersionMulti(cfg)
	if err != nil {
		t.Fatalf("MigrateVersionMulti: %v", err)
	}
	if dirty {
		t.Fatalf("schema_migrations dirty")
	}
	if v != 3 {
		t.Fatalf("expected version 3 (3 migrations across sources), got %d", v)
	}

	if err := pgkit.MigrateDownMulti(cfg); err != nil {
		t.Fatalf("MigrateDownMulti: %v", err)
	}
	v2, _, err := pgkit.MigrateVersionMulti(cfg)
	if err != nil {
		t.Fatalf("MigrateVersionMulti after down: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("expected version 2 after one down-step, got %d", v2)
	}
}

func TestComposeMigrations_MissingDown(t *testing.T) {
	src := fstest.MapFS{
		"migrations/0001_x.up.sql": {Data: []byte(`SELECT 1;`)},
	}
	cfg := pgkit.MultiSourceConfig{
		Sources: []pgkit.Source{{Name: "bad", FS: src, Prefix: "migrations"}},
	}
	err := pgkit.MigrateUpMulti(cfg)
	if err == nil {
		t.Fatalf("expected error for missing .down.sql, got nil")
	}
}

func TestComposeMigrations_NoSources(t *testing.T) {
	err := pgkit.MigrateUpMulti(pgkit.MultiSourceConfig{DSN: "x"})
	if err == nil {
		t.Fatalf("expected error for empty sources, got nil")
	}
}
