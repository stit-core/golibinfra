package pgkit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
			pgkit.MigrateRunnerConfig{Path: dir, Timeout: time.Minute},
		),
		pgkit.SupplyMigrations(pgkit.Source{Name: "b", FS: kit, Dir: "migrations"}),
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
	kit := fstest.MapFS{
		"migrations/0001_c.up.sql": {Data: []byte(`CREATE TABLE c_log (v INT);`)},
	}
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(pgkit.PoolConfig{DSN: pg.DSN}, pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute}),
		pgkit.SupplyMigrations(pgkit.Source{Name: "c", FS: kit, Dir: "migrations"}),
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

// Главное обещание advisory-lock'а: из одновременно стартующих инстансов
// миграцию применяет ровно один.
func TestMigrateUpAll_ConcurrentInstancesApplyOnce(t *testing.T) {
	const instances = 6
	kit := fstest.MapFS{
		"migrations/0001_e.up.sql": {Data: []byte(`CREATE TABLE e_log (v INT);`)},
		"migrations/0002_e.up.sql": {Data: []byte(`INSERT INTO e_log VALUES (1);`)},
	}
	src := pgkit.Source{Name: "e", FS: kit, Dir: "migrations"}

	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := make(chan struct{})
	errs := make([]error, instances)
	var wg sync.WaitGroup
	for i := range instances {
		wg.Go(func() {
			<-start
			errs[i] = pgkit.MigrateUpAll(ctx, pg.DSN, src)
		})
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("instance %d: MigrateUpAll: %v", i, err)
		}
	}
	var applied int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM e_log`).Scan(&applied); err != nil {
		t.Fatalf("count e_log: %v", err)
	}
	if applied != 1 {
		t.Fatalf("миграция применена %d раз, want ровно 1", applied)
	}
}

// Ожидание lock'а ограничено дедлайном ctx: MigrateUpAll не висит бесконечно.
func TestMigrateUpAll_LockWaitRespectsDeadline(t *testing.T) {
	pg := testkit.StartPostgres(t)
	pool := pg.Pool(t)

	holder, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer holder.Release()
	key := pgkit.AdvisoryLockKey()
	if _, err := holder.Exec(context.Background(), "SELECT pg_advisory_lock($1)", key); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	defer func() {
		_, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	}()

	kit := fstest.MapFS{"migrations/0001_f.up.sql": {Data: []byte(`CREATE TABLE f_log (v INT);`)}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = pgkit.MigrateUpAll(ctx, pg.DSN, pgkit.Source{Name: "f", FS: kit, Dir: "migrations"})
	if err == nil {
		t.Fatal("MigrateUpAll прошёл под удержанным lock'ом")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("ожидание lock'а заняло %v — дедлайн ctx не действует", elapsed)
	}
	if tableExists(t, pg.DB(t), "f_log") {
		t.Fatal("миграция применена, несмотря на удержанный lock")
	}
}

func TestMigrateVersion_FreshSourceIsZero(t *testing.T) {
	pg := testkit.StartPostgres(t)
	kit := fstest.MapFS{"migrations/0001_g.up.sql": {Data: []byte(`CREATE TABLE g_log (v INT);`)}}
	src := pgkit.Source{Name: "g", FS: kit, Dir: "migrations"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	v, dirty, err := pgkit.MigrateVersion(ctx, pg.DSN, src)
	if err != nil || dirty || v != 0 {
		t.Fatalf("MigrateVersion(fresh) = (%d, %v, %v), want (0, false, nil)", v, dirty, err)
	}
	if err := pgkit.MigrateUp(ctx, pg.DSN, src); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	if v := version(t, pg.DSN, src); v != 1 {
		t.Fatalf("version = %d, want 1", v)
	}
}

// Dirty-версия: MigrateVersion отдаёт её вместе с ошибкой «ручной ремонт».
func TestMigrateVersion_DirtyVersionIsAnError(t *testing.T) {
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)
	kit := fstest.MapFS{"migrations/0001_h.up.sql": {Data: []byte(`CREATE TABLE h_log (v INT);`)}}
	src := pgkit.Source{Name: "h", FS: kit, Dir: "migrations"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := pgkit.MigrateUp(ctx, pg.DSN, src); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	// Так выглядит прогон, прерванный на середине оператора.
	if _, err := db.Exec(ctx, `UPDATE `+src.VersionTable()+` SET dirty = true`); err != nil {
		t.Fatalf("mark dirty: %v", err)
	}

	v, dirty, err := pgkit.MigrateVersion(ctx, pg.DSN, src)
	if err == nil {
		t.Fatal("MigrateVersion на dirty-версии вернул nil")
	}
	if v != 1 || !dirty {
		t.Fatalf("MigrateVersion = (%d, %v), want (1, true)", v, dirty)
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("MigrateVersion err = %q, want упоминание dirty", err)
	}
}

// Предел имени источника — 45 символов: schema_migrations_<name> должен
// уложиться в 63 байта идентификатора Postgres.
func TestSource_NameLengthLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fsys := fstest.MapFS{"0001_x.up.sql": {Data: []byte(`SELECT 1;`)}}

	tooLong := pgkit.Source{Name: strings.Repeat("a", 46), FS: fsys, Dir: "."}
	err := pgkit.MigrateUp(ctx, "postgres://x", tooLong)
	if err == nil || !strings.Contains(err.Error(), "invalid source name") {
		t.Fatalf("MigrateUp(46 символов) err = %v, want invalid source name", err)
	}

	atLimit := pgkit.Source{Name: strings.Repeat("a", 45), FS: fsys, Dir: "."}
	if len(atLimit.VersionTable()) > 63 {
		t.Fatalf("VersionTable = %d байт, want <= 63", len(atLimit.VersionTable()))
	}
	// Имя валидно: дальше падает уже коннект, а не проверка имени.
	if err := pgkit.MigrateUp(ctx, "postgres://x", atLimit); err != nil &&
		strings.Contains(err.Error(), "invalid source name") {
		t.Fatalf("MigrateUp(45 символов) отвергнут по имени: %v", err)
	}
}
