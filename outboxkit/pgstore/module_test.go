package pgstore_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// noopStreamBus — natskit.StreamBus, которую fx-тестам достаточно иметь в графе.
type noopStreamBus struct{}

func (noopStreamBus) PublishStream(context.Context, string, []byte, ...natskit.PublishOption) error {
	return nil
}

func (noopStreamBus) PublishStreamBatch(_ context.Context, msgs []natskit.StreamMsg) ([]error, error) {
	return make([]error, len(msgs)), nil
}

func (noopStreamBus) SubscribeStream(context.Context, string, string, string, natskit.Handler) (natskit.Subscription, error) {
	return nil, nil
}

// scheduler — timerkit.Module с логгером: job'ы кита без него не собираются.
func scheduler() fx.Option {
	return fx.Options(fx.Provide(logkit.Nop), timerkit.Module)
}

// Регрессия FX-04: без timerkit.Module keeper-job'ы никто не запускает —
// граф обязан не собраться.
func TestModule_RequiresTimerkit(t *testing.T) {
	err := fx.ValidateApp(fx.NopLogger, fx.Supply(&pgkit.DB{}, pgstore.Config{}), pgstore.Module)
	if err == nil || !strings.Contains(err.Error(), "timerkit.scheduler") {
		t.Fatalf("pgstore.Module without timerkit.Module: err = %v, want missing timerkit.scheduler", err)
	}
}

func TestNewConfig_Defaults(t *testing.T) {
	cfg, err := pgstore.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	// tx-strict guard включён по умолчанию: Publish вне транзакции ломает
	// главный инвариант outbox'а.
	if !cfg.RequireTx {
		t.Fatal("OUTBOX_REQUIRE_TX default = false, want true")
	}
	if cfg.RetentionMonths != 0 {
		t.Fatalf("RetentionMonths default = %d, want 0", cfg.RetentionMonths)
	}
}

func TestNewConfig_NegativeRetentionIsErrConfig(t *testing.T) {
	t.Setenv("OUTBOX_RETENTION_MONTHS", "-1")
	_, err := pgstore.NewConfig()
	if !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_UnparsableValue(t *testing.T) {
	t.Setenv("OUTBOX_REQUIRE_TX", "sometimes")
	if _, err := pgstore.NewConfig(); err == nil {
		t.Fatal("expected error for unparsable OUTBOX_REQUIRE_TX")
	}
}

func TestMaintenanceJobs(t *testing.T) {
	s := pgstore.NewStore(nil)
	jobs := s.MaintenanceJobs()
	if len(jobs) != 1 || jobs[0].Name != "outboxkit.partition-keeper.outbox" {
		t.Fatalf("jobs = %v, want partition keeper only", jobNames(jobs))
	}
	// Партиции нужны с первого insert'а — keeper не ждёт сутки до первого прогона.
	if !jobs[0].RunOnStart {
		t.Fatal("partition keeper must run on start")
	}

	withRetention := pgstore.NewStore(nil, pgstore.WithRetention(3))
	if got := jobNames(withRetention.MaintenanceJobs()); len(got) != 2 || got[1] != "outboxkit.retention.outbox" {
		t.Fatalf("jobs = %v, want partition keeper + retention", got)
	}
}

func jobNames(jobs []timerkit.Job) []string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return names
}

// Партиции текущего и следующего месяца существуют сразу после старта Module,
// не дожидаясь суточного PartitionKeeperJob.
func TestModule_EnsuresPartitionsOnStart(t *testing.T) {
	db, _, ctx := startStore(t)

	curName := mustName(monthsFromNow(0))
	nextName := mustName(monthsFromNow(1))
	for _, name := range []string{curName, nextName} {
		if _, err := db.Exec(ctx, "DROP TABLE "+name); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	if partitionExists(t, ctx, db, curName) {
		t.Fatal("precondition: current partition must be absent")
	}

	app := fx.New(
		fx.NopLogger,
		fx.Supply(db, pgstore.Config{}),
		scheduler(),
		pgstore.Module,
	)
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for _, name := range []string{curName, nextName} {
		if !partitionExists(t, ctx, db, name) {
			t.Fatalf("partition %s missing after Module start", name)
		}
	}
}

// Без миграций кита Module не стартует на проверке схемы с именем источника и таблицы, а не на
// DDL партиции; Instance — так же, со своим источником (KX62).
func TestModule_ChecksSchemaOnStart(t *testing.T) {
	fix := testkit.StartPostgres(t)
	db := fix.DB(t)
	ctx := t.Context()

	single := fx.New(
		fx.NopLogger,
		fx.Supply(db, pgstore.Config{}),
		scheduler(),
		pgstore.Module,
	)
	err := single.Start(ctx)
	_ = single.Stop(context.Background())
	if !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), "table outbox") ||
		!strings.Contains(err.Error(), `source "outboxkit"`) {
		t.Fatalf("Module start without migrations = %v, want pgkit.ErrSchema naming the table and source", err)
	}

	instance := fx.New(
		fx.NopLogger,
		fx.Supply(db, pgstore.Config{}),
		fx.Provide(
			func() natskit.StreamBus { return &noopStreamBus{} },
			func() pgkit.Transactor { return fix.Transactor(t) },
			func() outboxkit.Config {
				return outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch, MaxAttempts: 25, TickTimeout: 30 * time.Second, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
			},
		),
		scheduler(),
		pgstore.FactoryModule,
		outboxkit.Instance("orders", "outbox_orders"),
	)
	err = instance.Start(ctx)
	_ = instance.Stop(context.Background())
	if !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), "instance orders") ||
		!strings.Contains(err.Error(), `source "outbox_orders"`) {
		t.Fatalf("Instance start without migrations = %v, want pgkit.ErrSchema naming the instance and source", err)
	}
}

func TestModule_RejectsInvalidConfig(t *testing.T) {
	db, _, ctx := startStore(t)
	app := fx.New(
		fx.NopLogger,
		fx.Supply(db, pgstore.Config{RetentionMonths: -1}),
		scheduler(),
		pgstore.Module,
		fx.Invoke(func(outboxkit.Repository) {}),
	)
	if err := app.Start(ctx); !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("Start err = %v, want ErrConfig", err)
	}
}

func TestModule_KeeperJobsRun(t *testing.T) {
	db, _, ctx := startStore(t)
	s := pgstore.NewStore(db)

	if err := pgstore.PartitionKeeperJob(s).Run(ctx); err != nil {
		t.Fatalf("PartitionKeeperJob: %v", err)
	}
	if err := pgstore.RetentionKeeperJob(s, 3).Run(ctx); err != nil {
		t.Fatalf("RetentionKeeperJob: %v", err)
	}
	if !partitionExists(t, ctx, db, mustName(monthsFromNow(1))) {
		t.Fatal("partition keeper did not create the next month partition")
	}
}

// Multi-instance путь целиком: схему таблицы накатывает pgkit.MigrateModule из
// группы, без ручного списка источников; фабрика даёт store, Instance — job'ы.
func TestFactoryModule_InstanceOnCustomTable(t *testing.T) {
	const table = "outbox_orders"
	fix, ctx := startFixture(t)
	db, tr := fix.DB(t), fix.Transactor(t)
	var exists bool
	if err := db.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if exists {
		t.Fatalf("precondition: table %s must be absent before start", table)
	}

	var pub outboxkit.Publisher
	var jobs []timerkit.Job
	app := fxtest.New(t,
		fx.Supply(
			db,
			pgstore.Config{RetentionMonths: 3, RequireTx: true},
			pgkit.PoolConfig{DSN: fix.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute},
		),
		fx.Provide(
			func() natskit.StreamBus { return &noopStreamBus{} },
			func() pgkit.Transactor { return tr },
			func() outboxkit.Config {
				return outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch, MaxAttempts: 25, TickTimeout: 30 * time.Second, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
			},
		),
		// Инстанс раньше MigrateModule и timerkit: проверку схемы и job'ы после миграций ставит
		// pgkit.Migrated, а не позиция.
		pgstore.FactoryModule,
		outboxkit.Instance("orders", table),
		pgkit.MigrateModule,
		scheduler(),
		fx.Invoke(fx.Annotate(func(p outboxkit.Publisher) { pub = p }, fx.ParamTags(`name:"orders"`))),
		fx.Invoke(fx.Annotate(func(js []timerkit.Job) { jobs = js }, fx.ParamTags(`group:"timerkit.jobs"`))),
	)
	app.RequireStart()
	defer app.RequireStop()

	names := jobNames(jobs)
	for _, want := range []string{"outboxkit.publisher.orders", "outboxkit.partition-keeper." + table, "outboxkit.retention." + table} {
		if !slices.Contains(names, want) {
			t.Fatalf("job %q missing: %v", want, names)
		}
	}

	// Конфиг фабрики применён: tx-strict guard включён.
	if err := pub.Publish(ctx, "order.created", sample{N: 1}); !errors.Is(err, outboxkit.ErrNoTx) {
		t.Fatalf("err = %v, want ErrNoTx", err)
	}
	if err := tr.WithTx(ctx, func(ctx context.Context) error {
		return pub.Publish(ctx, "order.created", sample{N: 1})
	}); err != nil {
		t.Fatalf("publish in tx: %v", err)
	}
	var n int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows in %s = %d, want 1", table, n)
	}
}

// Таблица outbox (Module) и инстансы (FactoryModule + Instance) живут в одном
// графе: MigrateModule накатывает обе схемы, публикация идёт каждая в свою.
func TestModule_CombinesWithFactoryModule(t *testing.T) {
	fix := testkit.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, tr := fix.DB(t), fix.Transactor(t)

	var single, orders outboxkit.Publisher
	app := fxtest.New(t,
		fx.Supply(
			db,
			pgstore.Config{RequireTx: true},
			pgkit.PoolConfig{DSN: fix.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute},
		),
		fx.Provide(
			func() natskit.StreamBus { return &noopStreamBus{} },
			func() pgkit.Transactor { return tr },
			func() outboxkit.Config {
				return outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch, MaxAttempts: 25, TickTimeout: 30 * time.Second, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
			},
		),
		scheduler(),
		pgkit.MigrateModule,
		pgstore.Module,
		outboxkit.Module,
		pgstore.FactoryModule,
		outboxkit.Instance("orders", "outbox_orders"),
		fx.Populate(&single),
		fx.Invoke(fx.Annotate(func(p outboxkit.Publisher) { orders = p }, fx.ParamTags(`name:"orders"`))),
	)
	app.RequireStart()
	defer app.RequireStop()

	for _, p := range []outboxkit.Publisher{single, orders} {
		if err := tr.WithTx(ctx, func(ctx context.Context) error {
			return p.Publish(ctx, "order.created", sample{N: 1})
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	for _, table := range []string{pgstore.DefaultTable, "outbox_orders"} {
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("rows in %s = %d, want 1", table, n)
		}
	}
}

// TableMigrations идемпотентны поверх таблицы, заведённой руками по SchemaSQL.
func TestTableMigrations_OverExistingTable(t *testing.T) {
	const table = "outbox_legacy"
	fix, ctx := startFixture(t)
	db := fix.DB(t)
	legacy := `CREATE TABLE IF NOT EXISTS outbox_legacy (
    id           BIGSERIAL    NOT NULL,
    subject      TEXT         NOT NULL,
    payload      JSONB        NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    attempts     INT          NOT NULL DEFAULT 0,
    last_error   TEXT,
    dead_at      TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);`
	if _, err := db.Exec(ctx, legacy); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}

	if err := pgkit.MigrateUpAll(ctx, fix.DSN, time.Minute, pgstore.TableMigrations(table)); err != nil {
		t.Fatalf("MigrateUpAll: %v", err)
	}
	for _, column := range []string{"next_attempt_at", "trace_context"} {
		var exists bool
		if err := db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
			table, column).Scan(&exists); err != nil {
			t.Fatalf("column check: %v", err)
		}
		if !exists {
			t.Fatalf("TableMigrations did not add %s to the legacy table", column)
		}
	}
}

// На чистой БД keeper-job'ы ждут миграций: RunOnStart иначе ловит отсутствующую
// таблицу и логирует ERROR, хотя порядок модулей в fx.New не должен решать.
func TestModule_KeeperJobsWaitForMigrations(t *testing.T) {
	fix := testkit.StartPostgres(t)
	db := fix.DB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rec := logtest.Capture()
	clock := clockkit.NewTickable(time.Now())

	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			func() logkit.Logger { return rec },
			func() clockkit.Clock { return clock },
		),
		// timerkit перечислен раньше миграций — порядок обеспечивает pgkit.Migrated.
		timerkit.Module,
		pgstore.Module,
		fx.Supply(
			db,
			pgstore.Config{RetentionMonths: 3},
			pgkit.PoolConfig{DSN: fix.DSN},
			pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Minute},
		),
		pgkit.MigrateModule,
	)
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = app.Stop(context.Background()) }()

	// Оба job'а поставили таймер — значит первый проход partition-keeper'а
	// (RunOnStart) уже завершён.
	clock.WaitTimers(2)

	for _, e := range rec.Entries() {
		if e.Level >= slog.LevelError {
			t.Fatalf("job на чистой БД дал %s: %s %v", e.Level, e.Msg, e.KV)
		}
	}
	if !partitionExists(t, ctx, db, mustName(monthsFromNow(0))) {
		t.Fatal("партиция текущего месяца не создана")
	}
}
