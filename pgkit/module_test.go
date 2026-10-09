package pgkit_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestModule_WiresGraphAndClosesPoolOnStop(t *testing.T) {
	pg := testkit.StartPostgres(t)

	var (
		db     *pgkit.DB
		tr     pgkit.Transactor
		rw     *pgkit.ReadWriteDB
		probes []healthkit.Probe
	)
	app := fxtest.New(t,
		fx.NopLogger,
		fx.Supply(pg.PoolConfig()),
		fx.Provide(logkit.Nop),
		pgkit.Module,
		healthkit.ProvideProbe(pgkit.Probe),
		healthkit.ProvideProbe(pgkit.ReplicaProbe),
		fx.Invoke(fx.Annotate(
			func(d *pgkit.DB, x pgkit.Transactor, r *pgkit.ReadWriteDB, p []healthkit.Probe) {
				db, tr, rw, probes = d, x, r, p
			},
			fx.ParamTags(``, ``, ``, `group:"healthkit.probes"`),
		)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app.RequireStart()

	if db == nil || tr == nil || rw == nil {
		t.Fatalf("граф неполон: db=%v tr=%v rw=%v", db, tr, rw)
	}
	// Без PG_REPLICA_DSN обе стороны — мастер.
	if rw.HasReplica() || rw.Read() != rw.Write() {
		t.Fatal("ReadWriteDB без реплики должен указывать обеими сторонами на мастер")
	}
	if len(probes) != 2 {
		t.Fatalf("проб в группе health: %d, want 2", len(probes))
	}
	for _, p := range probes {
		if err := p.Run(ctx); err != nil {
			t.Fatalf("проба %s: %v", p.Name, err)
		}
	}
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		_, err := db.Exec(ctx, `CREATE TABLE module_wiring (id INT PRIMARY KEY)`)
		return err
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	app.RequireStop()

	if _, err := db.Exec(ctx, `SELECT 1`); err == nil || !strings.Contains(err.Error(), "closed pool") {
		t.Fatalf("после Stop Exec err = %v, want closed pool", err)
	}
}

// Регрессия: callback pg_pool_* живёт от OnStart до OnStop — пул графа, который собран и не
// стартовал, не оставляет его в MeterProvider.
func TestModule_PoolMetricsLiveFromStartToStop(t *testing.T) {
	pg := testkit.StartPostgres(t)
	// Своя роль: серии primary и replica в провайдере процесса делят все тесты пакета.
	const role = "start-to-stop"
	lc := fxtest.NewLifecycle(t)
	pool, err := pgkit.OpenPool(lc, pg.PoolConfig(), role, logkit.Nop())
	if err != nil {
		t.Fatalf("openPool: %v", err)
	}
	// Хук остановки графа, не дошедшего до старта, не выполняется: пул закрывает тест.
	t.Cleanup(pool.Close)
	points := func() int { return len(metrictest.Collect(t).Find("pg_pool_max_conns", "role", role)) }

	if n := points(); n != 0 {
		t.Fatalf("built, not started: %d points observed, want 0", n)
	}
	lc.RequireStart()
	if n := points(); n != 1 {
		t.Fatalf("started: %d points observed, want 1", n)
	}
	lc.RequireStop()
	if n := points(); n != 0 {
		t.Fatalf("stopped: %d points observed, want 0", n)
	}
}

// Забытая транзакция не держит остановку пула до дедлайна: хук пула ждёт две трети бюджета,
// пишет WARN с числом занятых соединений, и хук после пула (телеметрия) выполняется (KFX-04).
func TestModule_StopWithAcquiredConnLeavesBudget(t *testing.T) {
	pg := testkit.StartPostgres(t)
	logs := logtest.Capture()
	var (
		pool     *pgxpool.Pool
		ranAfter atomic.Bool
	)
	app := fx.New(
		fx.NopLogger,
		// Модуль раньше pgkit.Module: его хук добавлен раньше и гасится после пула.
		fx.Module("after-pool", fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.Hook{OnStop: func(context.Context) error { ranAfter.Store(true); return nil }})
		})),
		fx.Supply(pg.PoolConfig()),
		fx.Provide(func() logkit.Logger { return logs }),
		pgkit.Module,
		fx.Populate(&pool),
	)
	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	begin := time.Now()
	err = app.Stop(stopCtx)
	took := time.Since(begin)
	if err != nil || !ranAfter.Load() {
		t.Fatalf("Stop = %v after %v, hook after the pool ran = %v; want nil and the hook run", err, took, ranAfter.Load())
	}
	if took > 2500*time.Millisecond {
		t.Fatalf("Stop took %v of a 3s budget: the pool hook ate the share of hooks after it", took)
	}
	warn, ok := logs.Find(slog.LevelWarn, "pgkit: pool close is still waiting for acquired connections")
	if !ok || warn.Str("role") != pgkit.RolePrimary {
		t.Fatalf("no WARN of the primary pool close: %+v", logs.Entries())
	}
	if n, _ := warn.Value("acquired_conns"); fmt.Sprint(n) != "1" {
		t.Fatalf("acquired_conns = %v, want 1", n)
	}
}

// ReplicaConfig не обязателен: Module поднимается и без него.
func TestModule_WithoutReplicaConfig(t *testing.T) {
	pg := testkit.StartPostgres(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(pg.PoolConfig()),
		fx.Provide(logkit.Nop),
		pgkit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start без ReplicaConfig: %v", err)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestModule_ReplicaPoolAndProbe(t *testing.T) {
	pg := testkit.StartPostgres(t)

	var rw *pgkit.ReadWriteDB
	app := fxtest.New(t,
		fx.NopLogger,
		fx.Supply(
			pg.PoolConfig(),
			pgkit.ReplicaConfig{ReplicaDSN: pg.DSN, MaxConns: 2},
		),
		fx.Provide(logkit.Nop),
		pgkit.Module,
		fx.Invoke(func(r *pgkit.ReadWriteDB) { rw = r }),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app.RequireStart()

	if !rw.HasReplica() || rw.Read() == rw.Write() {
		t.Fatal("PG_REPLICA_DSN задан, но отдельного пула реплики нет")
	}
	var one int
	if err := rw.Read().QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("запрос к реплике: %v", err)
	}
	if err := pgkit.ReplicaProbe(rw).Run(ctx); err != nil {
		t.Fatalf("ReplicaProbe: %v", err)
	}

	app.RequireStop()

	if _, err := rw.Read().Exec(ctx, `SELECT 1`); err == nil {
		t.Fatal("пул реплики не закрыт на OnStop")
	}
}

func TestModule_InvalidReplicaConfigFailsStart(t *testing.T) {
	pg := testkit.StartPostgres(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(pg.PoolConfig(), pgkit.ReplicaConfig{ReplicaDSN: pg.DSN, MaxConns: -1}),
		fx.Provide(logkit.Nop),
		pgkit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("Start err = %v, want ErrConfig", err)
	}
	_ = app.Stop(ctx)
}

// Зависимость от pgkit.Migrated ставит хук потребителя после миграций даже
// тогда, когда его модуль перечислен раньше MigrateModule.
func TestMigrateModule_MigratedOrdersConsumerHooks(t *testing.T) {
	pg := testkit.StartPostgres(t)
	db := pg.DB(t)

	kit := fstest.MapFS{
		"migrations/0001_d.up.sql": {Data: []byte(`CREATE TABLE d_log (v INT PRIMARY KEY);`)},
	}

	type consumer struct {
		fx.In
		LC       fx.Lifecycle
		Migrated pgkit.Migrated `optional:"true"`
	}
	var insertErr error
	consumerModule := fx.Module("schema-consumer", fx.Invoke(func(p consumer) {
		p.LC.Append(fx.Hook{OnStart: func(ctx context.Context) error {
			_, insertErr = db.Exec(ctx, `INSERT INTO d_log VALUES (1)`)
			return insertErr
		}})
	}))

	app := fx.New(
		fx.NopLogger,
		// Потребитель схемы стоит первым — порядок обеспечивает Migrated, не позиция.
		consumerModule,
		fx.Supply(
			pgkit.PoolConfig{DSN: pg.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute},
		),
		pgkit.SupplyMigrations(pgkit.Source{Name: "d", FS: kit, Dir: "migrations"}),
		pgkit.MigrateModule,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v (insert: %v)", err, insertErr)
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if insertErr != nil {
		t.Fatalf("хук потребителя выполнился до миграций: %v", insertErr)
	}
}

func TestMigrateModule_RejectsInvalidRunnerConfig(t *testing.T) {
	t.Parallel()
	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			pgkit.PoolConfig{DSN: "postgres://u@127.0.0.1:1/d"},
			pgkit.MigrateRunnerConfig{Path: "/migrations"},
		),
		pgkit.MigrateModule,
	)
	if err := app.Err(); !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("fx.New err = %v, want ErrConfig", err)
	}
}

// Кросс-инвариант реплики называет переменную, которую менял оператор.
func TestModule_ReplicaMaxConnsBelowMinConnsNamesReplicaEnv(t *testing.T) {
	pg := testkit.StartPostgres(t)
	poolCfg := pg.PoolConfig()
	poolCfg.MinConns = 2

	app := fx.New(
		fx.NopLogger,
		fx.Supply(poolCfg, pgkit.ReplicaConfig{ReplicaDSN: pg.DSN, MaxConns: 1}),
		fx.Provide(logkit.Nop),
		pgkit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("Start err = %v, want ErrConfig", err)
	}
	if !strings.Contains(err.Error(), "PG_REPLICA_MAX_CONNS (1) must be >= PG_MIN_CONNS (2)") {
		t.Fatalf("Start err = %q, want упоминание PG_REPLICA_MAX_CONNS", err)
	}
	_ = app.Stop(ctx)
}

// Падение миграций в OnStart валит старт приложения.
func TestMigrateModule_FailedMigrationFailsStart(t *testing.T) {
	pg := testkit.StartPostgres(t)
	broken := fstest.MapFS{
		"migrations/0001_i.up.sql": {Data: []byte(`CREATE TABLE i_log (v INT); SELECT no_such_function();`)},
	}

	app := fx.New(
		fx.NopLogger,
		fx.Supply(
			pgkit.PoolConfig{DSN: pg.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute},
		),
		pgkit.SupplyMigrations(pgkit.Source{Name: "i", FS: broken, Dir: "migrations"}),
		pgkit.MigrateModule,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if err == nil {
		t.Fatal("Start прошёл на сломанной миграции")
	}
	if !strings.Contains(err.Error(), "pgkit: migrate on start") {
		t.Fatalf("Start err = %q, want pgkit: migrate on start", err)
	}
	_ = app.Stop(ctx)
}

// Быстрый старт из README кита собирается: caller провайдит только конфиги, хук запроса берёт из
// графа pgkit.Migrated — очередь после миграций.
func TestQuickStartGraphValidates(t *testing.T) {
	t.Parallel()
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Provide(pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig, logkit.NewConfig),
		logkit.Module,
		pgkit.Module,
		pgkit.MigrateModule,
		healthkit.ProvideProbe(pgkit.Probe),
		healthkit.ProvideProbe(pgkit.ReplicaProbe),
		fx.Invoke(func(fx.Lifecycle, *pgkit.DB, pgkit.Transactor, pgkit.Migrated) {}),
	)
	if err != nil {
		t.Fatalf("быстрый старт README не собирается: %v", err)
	}
}
