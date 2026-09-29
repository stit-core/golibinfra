package outboxkit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

func deps(repo outboxkit.Repository, bus natskit.StreamBus, cfg outboxkit.Config) fx.Option {
	return fx.Provide(
		func() outboxkit.Repository { return repo },
		func() natskit.StreamBus { return bus },
		func() pgkit.Transactor { return &pgtest.Recorder{} },
		func() outboxkit.Config { return cfg },
	)
}

// scheduler — timerkit.Module с логгером: job'ы кита без него не собираются.
func scheduler() fx.Option {
	return fx.Options(fx.Provide(logkit.Nop), timerkit.Module)
}

func jobNames(jobs []timerkit.Job) []string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return names
}

func hasName(names []string, want string) bool {
	return slices.Contains(names, want)
}

func TestModule_WiresPublisherWorkerAndJob(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{msg(1, "a")},
		backlog:     []outboxkit.SubjectBacklog{{Subject: "a", Oldest: 2 * time.Second, Pending: 3}},
	}
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}

	var pub outboxkit.Publisher
	var jobs []timerkit.Job
	app := fxtest.New(t,
		deps(repo, &capturingBus{}, cfg),
		scheduler(),
		outboxkit.Module,
		fx.Populate(&pub),
		fx.Invoke(fx.Annotate(
			func(js []timerkit.Job) { jobs = js },
			fx.ParamTags(`group:"timerkit.jobs"`),
		)),
	)
	app.RequireStart()

	if pub == nil {
		t.Fatal("Publisher not provided")
	}
	if !hasName(jobNames(jobs), "outboxkit.publisher."+outboxkit.DefaultInstance) {
		t.Fatalf("drain job missing: %v", jobNames(jobs))
	}
	if jobs[0].Interval != cfg.Interval {
		t.Fatalf("job interval = %v, want %v", jobs[0].Interval, cfg.Interval)
	}
	// Backlog-gauge'и подключаются автоматически, пока приложение живо.
	if n := gaugePoints(collect(), "outbox_pending_count"); n != 1 {
		t.Fatalf("outbox_pending_count points = %d, want 1", n)
	}

	app.RequireStop()
	if n := gaugePoints(collect(), "outbox_pending_count"); n != 0 {
		t.Fatalf("gauges still observed after stop: %d points", n)
	}
}

// Репозиторий без BacklogReporter — модуль поднимается, gauge'ей просто нет.
func TestModule_WithoutBacklogReporter(t *testing.T) {
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
	app := fxtest.New(t,
		deps(bareRepo{repo: &fakeRepo{}}, &capturingBus{}, cfg),
		scheduler(),
		outboxkit.Module,
		fx.Invoke(func(*outboxkit.Worker) {}),
	)
	app.RequireStart()
	app.RequireStop()
}

func TestModule_ValidateApp(t *testing.T) {
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
	err := fx.ValidateApp(
		fx.NopLogger,
		deps(&fakeRepo{}, &capturingBus{}, cfg),
		scheduler(),
		outboxkit.Module,
		fx.Invoke(func(outboxkit.Publisher, *outboxkit.Worker, *outboxkit.Bus) {}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Регрессия FX-04: без timerkit.Module drain-job никто не запускает — граф
// обязан не собраться, а не держать outbox молча стоящим.
func TestModule_RequiresTimerkit(t *testing.T) {
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
	err := fx.ValidateApp(fx.NopLogger, deps(&fakeRepo{}, &capturingBus{}, cfg), outboxkit.Module)
	if err == nil || !strings.Contains(err.Error(), "timerkit.Scheduler") {
		t.Fatalf("outboxkit.Module without timerkit.Module: err = %v, want missing timerkit.Scheduler", err)
	}
	err = fx.ValidateApp(fx.NopLogger,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return &fakeFactory{repo: &fakeRepo{}} },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config { return cfg },
		),
		outboxkit.Instance("orders", "outbox_orders"),
	)
	if err == nil || !strings.Contains(err.Error(), "timerkit.Scheduler") {
		t.Fatalf("outboxkit.Instance without timerkit.Module: err = %v, want missing timerkit.Scheduler", err)
	}
}

func TestInstance_WiresNamedOutbox(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{msg(1, "order.created")},
		backlog:     []outboxkit.SubjectBacklog{{Subject: "order.created", Oldest: time.Second, Pending: 1}},
	}
	factory := &fakeFactory{repo: repo}
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}

	var pub outboxkit.Publisher
	var jobs []timerkit.Job
	app := fxtest.New(t,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return factory },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config { return cfg },
		),
		scheduler(),
		outboxkit.Instance("orders", "outbox_orders"),
		fx.Invoke(fx.Annotate(func(p outboxkit.Publisher) { pub = p }, fx.ParamTags(`name:"orders"`))),
		fx.Invoke(fx.Annotate(func(js []timerkit.Job) { jobs = js }, fx.ParamTags(`group:"timerkit.jobs"`))),
	)
	app.RequireStart()

	if factory.table != "outbox_orders" {
		t.Fatalf("factory table = %q, want outbox_orders", factory.table)
	}
	if pub == nil {
		t.Fatal("named Publisher not provided")
	}
	names := jobNames(jobs)
	if !hasName(names, "outboxkit.publisher.orders") {
		t.Fatalf("drain job missing: %v", names)
	}
	// Job'ы обслуживания хранилища приезжают вместе с инстансом.
	if !hasName(names, "fake.maintenance") {
		t.Fatalf("maintenance job missing: %v", names)
	}
	if n := gaugePoints(collect(), "outbox_lag_seconds"); n != 1 {
		t.Fatalf("outbox_lag_seconds points = %d, want 1", n)
	}
	app.RequireStop()
}

// Регрессия FX-12: схема таблицы инстанса приезжает в группу pgkit.migrations
// сама — pgkit.MigrateModule накатывает её без ручного списка источников.
func TestInstance_ProvidesMigrations(t *testing.T) {
	cfg := outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
	var names []string
	fxtest.New(t,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return &fakeFactory{repo: bareRepo{repo: &fakeRepo{}}} },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config { return cfg },
		),
		scheduler(),
		outboxkit.Instance("orders", "outbox_orders"),
		outboxkit.Instance("leads", "outbox_leads"),
		fx.Invoke(fx.Annotate(
			func(srcs []pgkit.Source) {
				for _, s := range srcs {
					names = append(names, s.VersionTable())
				}
			},
			fx.ParamTags(`group:"`+pgkit.MigrationsGroup+`"`),
		)),
	)

	slices.Sort(names)
	want := []string{"schema_migrations_outbox_leads", "schema_migrations_outbox_orders"}
	if !slices.Equal(names, want) {
		t.Fatalf("migration sources = %v, want %v", names, want)
	}
}

func TestInstance_FactoryErrorFailsStart(t *testing.T) {
	factory := &fakeFactory{err: errors.New("bad table")}
	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return factory },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config {
				return outboxkit.Config{BatchSize: 1, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
			},
		),
		scheduler(),
		outboxkit.Instance("orders", "Bad Table"),
	)
	if err := app.Err(); err == nil {
		t.Fatal("expected app error from repository factory")
	}
}

func TestInstance_EmptyNameRejected(t *testing.T) {
	if err := fx.New(fx.NopLogger, outboxkit.Instance("", "outbox_orders")).Err(); !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestInstance_ConfigOverride(t *testing.T) {
	repo := &fakeRepo{}
	factory := &fakeFactory{repo: repo}
	override := outboxkit.Config{BatchSize: 7, Interval: 5 * time.Second, PublishMode: outboxkit.PublishOrdered}

	var ob *outboxkit.Outbox
	app := fxtest.New(t,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return factory },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config {
				return outboxkit.Config{BatchSize: 100, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
			},
		),
		scheduler(),
		outboxkit.Instance("orders", "outbox_orders", outboxkit.WithInstanceConfig(override)),
		fx.Invoke(fx.Annotate(func(o *outboxkit.Outbox) { ob = o }, fx.ParamTags(`name:"orders"`))),
	)
	app.RequireStart()
	defer app.RequireStop()

	if ob.Job().Interval != override.Interval {
		t.Fatalf("job interval = %v, want %v", ob.Job().Interval, override.Interval)
	}
	if err := ob.Worker().Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if repo.listMode != outboxkit.PublishOrdered || repo.limit != 7 {
		t.Fatalf("instance config not applied: mode=%q limit=%d", repo.listMode, repo.limit)
	}
}

func TestOutbox_ReviveDead(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{revived: 3}
	o := outboxkit.New("orders", repo, &fakeTransport{}, &pgtest.Recorder{},
		outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})

	filter := outboxkit.ReviveFilter{IDs: []int64{1, 2, 3}, Subject: "order.created"}
	n, err := o.ReviveDead(context.Background(), filter)
	if err != nil {
		t.Fatalf("ReviveDead: %v", err)
	}
	if n != 3 {
		t.Fatalf("revived = %d, want 3", n)
	}
	if repo.reviveFilter.Subject != "order.created" || len(repo.reviveFilter.IDs) != 3 {
		t.Fatalf("filter not passed through: %+v", repo.reviveFilter)
	}
	if got := counterValue(collect(), "outbox_revived_total", map[string]string{"instance": "orders"}); got != 3 {
		t.Fatalf("outbox_revived_total = %d, want 3", got)
	}
}

func TestOutbox_ReviveDeadUnsupported(t *testing.T) {
	o := outboxkit.New("orders", bareRepo{repo: &fakeRepo{}}, &fakeTransport{}, &pgtest.Recorder{},
		outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})

	if _, err := o.ReviveDead(context.Background(), outboxkit.ReviveFilter{}); !errors.Is(err, outboxkit.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
	// Рецепт из usage.md — `defer reg.Unregister()` без проверки на nil —
	// обязан работать и на репозитории без BacklogReporter.
	reg, err := o.RegisterBacklogGauges()
	if err != nil {
		t.Fatalf("RegisterBacklogGauges on plain repo: %v", err)
	}
	if reg == nil {
		t.Fatal("RegisterBacklogGauges вернула nil Registration — defer reg.Unregister() паникует")
	}
	if err := reg.Unregister(); err != nil {
		t.Fatalf("Unregister on no-op registration: %v", err)
	}
	if o.Name() != "orders" {
		t.Fatalf("Name = %q", o.Name())
	}
	if len(o.Jobs()) != 1 {
		t.Fatalf("Jobs = %d, want 1 (repo without Maintainer)", len(o.Jobs()))
	}
}

func TestOutbox_ReviveDeadError(t *testing.T) {
	repo := &fakeRepo{reviveErr: errors.New("db down")}
	o := outboxkit.New("orders", repo, &fakeTransport{}, &pgtest.Recorder{},
		outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})

	if _, err := o.ReviveDead(context.Background(), outboxkit.ReviveFilter{}); err == nil {
		t.Fatal("expected error from repository")
	}
}

// Быстрый старт из README собирается: caller провайдит только конфиги китов.
func TestQuickStartGraphValidates(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Provide(
			logkit.NewConfig,
			pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig,
			natskit.NewConfig,
			pgstore.NewConfig, outboxkit.NewConfig,
		),
		logkit.Module,
		pgkit.Module,
		pgkit.MigrateModule,
		natskit.Module(""),
		timerkit.Module,
		pgstore.Module,
		outboxkit.Module,
		fx.Invoke(func(outboxkit.Publisher) {}),
	)
	if err != nil {
		t.Fatalf("README quick start does not resolve: %v", err)
	}
}

// Multi-instance-рецепт из usage.md собирается тем же способом.
func TestInstanceRecipeValidates(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Provide(
			logkit.NewConfig,
			pgkit.NewPoolConfig, pgkit.NewMigrateRunnerConfig,
			natskit.NewConfig,
			pgstore.NewConfig, outboxkit.NewConfig,
		),
		logkit.Module,
		pgkit.Module,
		pgkit.MigrateModule,
		natskit.Module(""),
		timerkit.Module,
		pgstore.FactoryModule,
		outboxkit.Instance("orders", "outbox_orders"),
		fx.Invoke(fx.Annotate(func(outboxkit.Publisher) {}, fx.ParamTags(`name:"orders"`))),
	)
	if err != nil {
		t.Fatalf("multi-instance recipe does not resolve: %v", err)
	}
}
