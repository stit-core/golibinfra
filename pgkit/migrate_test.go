package pgkit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// lockTimeout — lock timeout миграций теста: чужих сессий у контейнера нет.
const lockTimeout = time.Minute

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
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, lockTimeout, kitSrc, appSrc); err != nil {
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
	if err := pgkit.MigrateUpAll(ctx, pg.DSN, lockTimeout, kitSrc, appSrc); err != nil {
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

	if err := pgkit.MigrateDown(ctx, pg.DSN, lockTimeout, kitSrc); err != nil {
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
			pgkit.MigrateRunnerConfig{Path: dir, Timeout: time.Minute, LockTimeout: time.Minute},
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

// Регрессия RAPI-04: каталога миграций приложения нет или он пуст — ошибка старта называет
// каталог и переменную PG_MIGRATIONS_PATH (и как запускаться без своих миграций), а не
// «failed to init driver with path .». До базы дело не доходит.
func TestMigrateModule_MissingAppDirNamesTheVariable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "migrations")
	for name, dir := range map[string]string{"missing": missing, "empty": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			app := fx.New(
				fx.NopLogger,
				fx.Supply(
					pgkit.PoolConfig{DSN: "postgres://app@127.0.0.1:1/app"},
					pgkit.MigrateRunnerConfig{Path: dir, Timeout: time.Minute, LockTimeout: time.Minute},
				),
				pgkit.MigrateModule,
			)
			err := app.Start(context.Background())
			if err == nil {
				t.Fatal("Start = nil, want the missing application migrations to fail the start")
			}
			for _, want := range []string{"pgkit: migrate on start: PG_MIGRATIONS_PATH", dir, "PG_MIGRATIONS_PATH=-"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("start error %q does not name %q", err, want)
				}
			}
		})
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
		fx.Supply(pgkit.PoolConfig{DSN: pg.DSN}, pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute}),
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
		if err := pgkit.MigrateUp(ctx, "postgres://x", lockTimeout, pgkit.Source{Name: name, FS: fsys, Dir: "."}); err == nil {
			t.Fatalf("source name %q accepted", name)
		}
	}
	if err := pgkit.MigrateUp(ctx, "postgres://x", lockTimeout, pgkit.Source{Name: "nofs"}); err == nil {
		t.Fatal("nil FS accepted")
	}
	if err := pgkit.MigrateUp(ctx, "postgres://x", lockTimeout, pgkit.Source{Name: "empty", FS: fstest.MapFS{}, Dir: "."}); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := pgkit.MigrateUpAll(ctx, "postgres://x", lockTimeout,
		pgkit.Source{Name: "dup", FS: fsys, Dir: "."},
		pgkit.Source{Name: "dup", FS: fsys, Dir: "."},
	); err == nil {
		t.Fatal("duplicate source names accepted")
	}
	if err := pgkit.MigrateUpAll(ctx, "postgres://x", lockTimeout); err != nil {
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
			errs[i] = pgkit.MigrateUpAll(ctx, pg.DSN, lockTimeout, src)
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
	err = pgkit.MigrateUpAll(ctx, pg.DSN, lockTimeout, pgkit.Source{Name: "f", FS: kit, Dir: "migrations"})
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
	if err := pgkit.MigrateUp(ctx, pg.DSN, lockTimeout, src); err != nil {
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

	if err := pgkit.MigrateUp(ctx, pg.DSN, lockTimeout, src); err != nil {
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
	err := pgkit.MigrateUp(ctx, "postgres://x", lockTimeout, tooLong)
	if err == nil || !strings.Contains(err.Error(), "invalid source name") {
		t.Fatalf("MigrateUp(46 символов) err = %v, want invalid source name", err)
	}

	atLimit := pgkit.Source{Name: strings.Repeat("a", 45), FS: fsys, Dir: "."}
	if len(atLimit.VersionTable()) > 63 {
		t.Fatalf("VersionTable = %d байт, want <= 63", len(atLimit.VersionTable()))
	}
	// Имя валидно: дальше падает уже коннект, а не проверка имени.
	if err := pgkit.MigrateUp(ctx, "postgres://x", lockTimeout, atLimit); err != nil &&
		strings.Contains(err.Error(), "invalid source name") {
		t.Fatalf("MigrateUp(45 символов) отвергнут по имени: %v", err)
	}
}

// lockedTable держит AccessShareLock на таблице открытой транзакцией до конца теста: ALTER
// миграции встаёт за ней в очередь, а читатели таблицы — за ALTER.
func lockedTable(t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE `+table+` (id INT); INSERT INTO `+table+` VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(ctx, `SELECT * FROM `+table); err != nil {
		t.Fatal(err)
	}
}

// readWait — сколько читатель таблицы, пришедший через after после старта миграции, ждал ответа.
func readWait(t *testing.T, pool *pgxpool.Pool, table string, after time.Duration) (time.Duration, error) {
	t.Helper()
	time.Sleep(after)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
	return time.Since(start), err
}

// lock_timeout мигратора: ALTER под чужой блокировкой сдаётся через lock timeout, а не держит
// очередь читателей таблицы до конца чужой транзакции (KCOR-03).
func TestMigrateUpAll_LockTimeoutReleasesReaders(t *testing.T) {
	pg := testkit.StartPostgres(t)
	pool := pg.Pool(t)
	lockedTable(t, pool, "lt_t")
	src := pgkit.Source{Name: "lt", FS: fstest.MapFS{
		"m/0001_add.up.sql": {Data: []byte(`ALTER TABLE lt_t ADD COLUMN c INT;`)},
	}, Dir: "m"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- pgkit.MigrateUpAll(ctx, pg.DSN, time.Second, src) }()
	wait, readErr := readWait(t, pool, "lt_t", 300*time.Millisecond)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("MigrateUpAll = %v, want a lock timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("MigrateUpAll returned after %v, want about the 1s lock timeout", elapsed)
	}
	if readErr != nil || wait > 3*time.Second {
		t.Fatalf("reader waited %v (err %v), want no longer than the lock timeout", wait, readErr)
	}
}

// MigrateModule берёт lock timeout из PG_MIGRATE_LOCK_TIMEOUT: ALTER под чужой блокировкой
// валит старт через него, а не через PG_MIGRATE_TIMEOUT.
func TestMigrateModule_UsesLockTimeout(t *testing.T) {
	pg := testkit.StartPostgres(t)
	lockedTable(t, pg.Pool(t), "mlt_t")
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			pgkit.PoolConfig{DSN: pg.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Second},
		),
		pgkit.SupplyMigrations(pgkit.Source{Name: "mlt", FS: fstest.MapFS{
			"m/0001_add.up.sql": {Data: []byte(`ALTER TABLE mlt_t ADD COLUMN c INT;`)},
		}, Dir: "m"}),
		pgkit.MigrateModule,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	err := app.Start(ctx)
	_ = app.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("Start = %v, want a lock timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Start failed after %v, want about the 1s PG_MIGRATE_LOCK_TIMEOUT", elapsed)
	}
}

// Дедлайн ctx прерывает идущий оператор на сервере, а не только обрывает сокет: прогон
// возвращается к дедлайну с context.DeadlineExceeded, сервер оператор больше не исполняет.
func TestMigrateUp_DeadlineCancelsRunningStatement(t *testing.T) {
	pg := testkit.StartPostgres(t)
	pool := pg.Pool(t)
	src := pgkit.Source{Name: "dl", FS: fstest.MapFS{
		"m/0001_sleep.up.sql": {Data: []byte(`SELECT pg_sleep(30); CREATE TABLE dl_never (v INT);`)},
	}, Dir: "m"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := pgkit.MigrateUp(ctx, pg.DSN, lockTimeout, src)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("MigrateUp = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("MigrateUp returned after %v at a 2s deadline", elapsed)
	}
	running := -1
	for range 20 {
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
WHERE state = 'active' AND pid <> pg_backend_pid() AND query LIKE '%pg_sleep(30)%'`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		if running == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if running != 0 {
		t.Fatalf("the server still runs the migration statement after the deadline (%d sessions)", running)
	}
	if tableExists(t, pg.DB(t), "dl_never") {
		t.Fatal("the interrupted migration was applied")
	}
}

// gateFS отдаёт файл gated только после release: дедлайн истекает между миграциями, когда
// оператора миграции на сервере нет.
type gateFS struct {
	fs.FS
	gated   string
	release <-chan struct{}
}

func (g gateFS) Open(name string) (fs.File, error) {
	if name == g.gated {
		<-g.release
	}
	return g.FS.Open(name)
}

// Дедлайн между миграциями прерывает и следующую: её оператор, начатый после дедлайна, сервер
// не доводит до конца, а прогон возвращает ошибку, а не nil.
func TestMigrateUp_DeadlineBetweenMigrationsStopsTheNext(t *testing.T) {
	pg := testkit.StartPostgres(t)
	release := make(chan struct{})
	src := pgkit.Source{Name: "gs", FS: gateFS{FS: fstest.MapFS{
		"m/0001_a.up.sql": {Data: []byte(`CREATE TABLE gs_a (v INT);`)},
		"m/0002_b.up.sql": {Data: []byte(`SELECT pg_sleep(5); CREATE TABLE gs_b (v INT);`)},
	}, gated: "m/0002_b.up.sql", release: release}, Dir: "m"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()
	start := time.Now()
	err := pgkit.MigrateUp(ctx, pg.DSN, lockTimeout, src)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("MigrateUp = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("MigrateUp returned after %v at a 2s deadline", elapsed)
	}
	db := pg.DB(t)
	if !tableExists(t, db, "gs_a") || tableExists(t, db, "gs_b") {
		t.Fatalf("gs_a=%v gs_b=%v, want the first migration applied and the next one stopped",
			tableExists(t, db, "gs_a"), tableExists(t, db, "gs_b"))
	}
}

// Неположительный lock timeout отвергается до коннекта: «без предела» — не ноль из литерала.
func TestMigrate_RejectsNonPositiveLockTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := pgkit.Source{Name: "lt", FS: fstest.MapFS{"0001_x.up.sql": {Data: []byte(`SELECT 1;`)}}, Dir: "."}
	for _, d := range []time.Duration{0, -time.Second} {
		for name, err := range map[string]error{
			"MigrateUp":    pgkit.MigrateUp(ctx, "postgres://x", d, src),
			"MigrateDown":  pgkit.MigrateDown(ctx, "postgres://x", d, src),
			"MigrateUpAll": pgkit.MigrateUpAll(ctx, "postgres://x", d, src),
		} {
			if !errors.Is(err, pgkit.ErrConfig) || !strings.Contains(err.Error(), "lock timeout must be > 0") {
				t.Fatalf("%s(lock timeout %s) = %v, want the lock timeout error", name, d, err)
			}
		}
	}
}
