package testkit

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// MigrationLadder прогоняет на чистом Postgres лесенку миграций источников из
// WithMigrations/WithMigrationsDir: каждая версия — up → down → up (down обязан
// вернуть схему к состоянию до своего up, повторный up — воспроизвести схему
// версии), затем весь источник — down до нуля (остаются только таблицы версий)
// и снова up. Источник идёт поверх предыдущих, как в pgkit.MigrateUpAll.
// Расширения не сравниваются: down не обязан удалять общее расширение.
func MigrationLadder(t testing.TB, opts ...Option) {
	t.Helper()
	o := newOptions(t, support{helper: "MigrationLadder", migrations: true}, PostgresImage, opts)
	if len(o.sources) == 0 {
		t.Fatalf("testkit: MigrationLadder needs WithMigrations or WithMigrationsDir")
	}
	pg := StartPostgres(t, WithImage(o.image))
	pool := pg.Pool(t)
	for _, src := range o.sources {
		ladder(t, pg.DSN, pool, src)
	}
}

func ladder(t testing.TB, dsn string, pool *pgxpool.Pool, src pgkit.Source) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), migrateTimeout)
	defer cancel()
	migrate := func(err error, op string, v uint64) {
		t.Helper()
		if err != nil {
			t.Fatalf("testkit: ladder %s: %s %d: %v", src.Name, op, v, err)
		}
	}

	files := migrationFiles(t, src)
	versions := slices.Sorted(maps.Keys(files))
	base := schemaSnapshot(t, pool)
	for _, v := range versions {
		step := sourceUpTo(t, src, files, v)
		before := schemaSnapshot(t, pool)
		migrate(pgkit.MigrateUp(ctx, dsn, migrateTimeout, step), "up", v)
		after := schemaSnapshot(t, pool)
		migrate(pgkit.MigrateDown(ctx, dsn, migrateTimeout, step), "down", v)
		sameSchema(t, before, schemaSnapshot(t, pool), fmt.Sprintf("%s: down %d must restore the schema it started from", src.Name, v))
		migrate(pgkit.MigrateUp(ctx, dsn, migrateTimeout, step), "up after down", v)
		sameSchema(t, after, schemaSnapshot(t, pool), fmt.Sprintf("%s: up %d after down must rebuild the same schema", src.Name, v))
	}

	top := schemaSnapshot(t, pool)
	for _, v := range slices.Backward(versions) {
		migrate(pgkit.MigrateDown(ctx, dsn, migrateTimeout, src), "down to zero at", v)
	}
	if version, _, err := pgkit.MigrateVersion(ctx, dsn, src); err != nil || version != 0 {
		t.Fatalf("testkit: ladder %s: version after down to zero = %d, %v; want 0", src.Name, version, err)
	}
	sameSchema(t, base, schemaSnapshot(t, pool), src.Name+": down to zero must restore the schema before the source")
	migrate(pgkit.MigrateUp(ctx, dsn, migrateTimeout, src), "up after down to zero, top", versions[len(versions)-1])
	sameSchema(t, top, schemaSnapshot(t, pool), src.Name+": up after down to zero must rebuild the same schema")
}

// migrationRe — имя файла миграции по правилу golang-migrate: файлы вне него
// источник не видит, лесенка тоже.
var migrationRe = regexp.MustCompile(`^([0-9]+)_.*\.(up|down)\.[^.]+$`)

func migrationFiles(t testing.TB, src pgkit.Source) map[uint64][]string {
	t.Helper()
	entries, err := fs.ReadDir(src.FS, src.Dir)
	if err != nil {
		t.Fatalf("testkit: ladder %s: read migrations: %v", src.Name, err)
	}
	files := map[uint64][]string{}
	for _, e := range entries {
		m := migrationRe.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatalf("testkit: ladder %s: version of %s: %v", src.Name, e.Name(), err)
		}
		files[v] = append(files[v], e.Name())
	}
	if len(files) == 0 {
		t.Fatalf("testkit: ladder %s: no migrations in %q", src.Name, src.Dir)
	}
	return files
}

// sourceUpTo — тот же источник (та же таблица версий) только с версиями до v
// включительно: MigrateUp доходит ровно до v.
func sourceUpTo(t testing.TB, src pgkit.Source, files map[uint64][]string, v uint64) pgkit.Source {
	t.Helper()
	upTo := fstest.MapFS{}
	for version, names := range files {
		if version > v {
			continue
		}
		for _, name := range names {
			data, err := fs.ReadFile(src.FS, path.Join(src.Dir, name))
			if err != nil {
				t.Fatalf("testkit: ladder %s: read %s: %v", src.Name, name, err)
			}
			upTo[name] = &fstest.MapFile{Data: data}
		}
	}
	return pgkit.Source{Name: src.Name, FS: upTo, Dir: "."}
}

// schemaQuery — отпечаток схемы из pg_catalog: схемы, отношения (с ключом и
// границами партиций), колонки, индексы, ограничения, триггеры, функции, типы и
// тексты представлений; без системных схем, таблиц версий schema_migrations_* и
// объектов расширений.
const schemaQuery = `
WITH ns AS (
	SELECT oid, nspname FROM pg_namespace
	WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname NOT LIKE 'pg\_%'
), rel AS (
	SELECT c.oid, c.relname, c.relkind, c.relpersistence, c.relpartbound, ns.nspname
	FROM pg_class c JOIN ns ON ns.oid = c.relnamespace
	WHERE c.relname NOT LIKE 'schema\_migrations\_%'
	  AND NOT EXISTS (SELECT 1 FROM pg_depend d
	                  WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
)
SELECT 'schema ' || nspname FROM ns
UNION ALL
SELECT format('relation %I.%I kind=%s%s%s%s', nspname, relname, relkind, relpersistence,
              ' partition by ' || pg_get_partkeydef(oid), ' ' || pg_get_expr(relpartbound, oid))
FROM rel
UNION ALL
SELECT format('column %I.%I.%I %s notnull=%s default=%s identity=%s generated=%s',
              rel.nspname, rel.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
              pg_get_expr(ad.adbin, ad.adrelid), a.attidentity, a.attgenerated)
FROM pg_attribute a
JOIN rel ON rel.oid = a.attrelid AND rel.relkind IN ('r', 'p', 'v', 'm', 'f')
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT 'index ' || pg_get_indexdef(i.indexrelid) || CASE WHEN i.indisvalid THEN '' ELSE ' INVALID' END
FROM pg_index i JOIN rel ON rel.oid = i.indexrelid
UNION ALL
SELECT format('constraint %I.%I on %s %s', ns.nspname, co.conname,
              CASE WHEN co.conrelid = 0 THEN co.contypid::regtype::text ELSE co.conrelid::regclass::text END,
              pg_get_constraintdef(co.oid))
FROM pg_constraint co JOIN ns ON ns.oid = co.connamespace
WHERE co.conrelid = 0 OR co.conrelid IN (SELECT oid FROM rel)
UNION ALL
SELECT 'trigger ' || pg_get_triggerdef(tg.oid)
FROM pg_trigger tg JOIN rel ON rel.oid = tg.tgrelid
WHERE NOT tg.tgisinternal
UNION ALL
SELECT format('function %s returns %s source %s', p.oid::regprocedure, pg_get_function_result(p.oid), md5(p.prosrc))
FROM pg_proc p JOIN ns ON ns.oid = p.pronamespace
WHERE NOT EXISTS (SELECT 1 FROM pg_depend d
                  WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
UNION ALL
SELECT format('type %s %s %s', ty.oid::regtype, ty.typtype,
              coalesce((SELECT string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = ty.oid),
                       format_type(ty.typbasetype, ty.typtypmod)))
FROM pg_type ty JOIN ns ON ns.oid = ty.typnamespace
WHERE ty.typtype IN ('e', 'd', 'r', 'm')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d
                  WHERE d.classid = 'pg_type'::regclass AND d.objid = ty.oid AND d.deptype = 'e')
UNION ALL
SELECT format('view %I.%I %s', nspname, relname, md5(pg_get_viewdef(oid)))
FROM rel WHERE relkind IN ('v', 'm')
ORDER BY 1`

func schemaSnapshot(t testing.TB, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), schemaQuery)
	if err != nil {
		t.Fatalf("testkit: ladder: schema snapshot: %v", err)
	}
	objects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("testkit: ladder: schema snapshot: %v", err)
	}
	return objects
}

// sameSchema падает с разницей снимков: «-» — было и пропало, «+» — появилось.
func sameSchema(t testing.TB, want, got []string, msg string) {
	t.Helper()
	if slices.Equal(want, got) {
		return
	}
	var diff []string
	for _, s := range want {
		if !slices.Contains(got, s) {
			diff = append(diff, "- "+s)
		}
	}
	for _, s := range got {
		if !slices.Contains(want, s) {
			diff = append(diff, "+ "+s)
		}
	}
	t.Fatalf("testkit: ladder %s:\n%s", msg, strings.Join(diff, "\n"))
}
