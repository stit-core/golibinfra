package outboxkit_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
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
	collect := metricsSince(t)
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{msg(1, "a")},
		backlog:     []outboxkit.SubjectBacklog{{Subject: "a", Oldest: 2 * time.Second, Pending: 3}},
	}
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})

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
	if n := len(collect().Find("outbox_pending_count", "instance", outboxkit.DefaultInstance)); n != 1 {
		t.Fatalf("outbox_pending_count points = %d, want 1", n)
	}

	app.RequireStop()
	if n := len(collect().Find("outbox_pending_count", "instance", outboxkit.DefaultInstance)); n != 0 {
		t.Fatalf("gauges still observed after stop: %d points", n)
	}
}

// Репозиторий без BacklogReporter — модуль поднимается, gauge'ей просто нет.
func TestModule_WithoutBacklogReporter(t *testing.T) {
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
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
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
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
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
	err := fx.ValidateApp(fx.NopLogger, deps(&fakeRepo{}, &capturingBus{}, cfg), outboxkit.Module)
	if err == nil || !strings.Contains(err.Error(), "timerkit.scheduler") {
		t.Fatalf("outboxkit.Module without timerkit.Module: err = %v, want missing timerkit.scheduler", err)
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
	if err == nil || !strings.Contains(err.Error(), "timerkit.scheduler") {
		t.Fatalf("outboxkit.Instance without timerkit.Module: err = %v, want missing timerkit.scheduler", err)
	}
}

func TestInstance_WiresNamedOutbox(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{msg(1, "order.created")},
		backlog:     []outboxkit.SubjectBacklog{{Subject: "order.created", Oldest: time.Second, Pending: 1}},
	}
	factory := &fakeFactory{repo: repo}
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})

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
	if n := len(collect().Find("outbox_lag_seconds", "instance", "orders")); n != 1 {
		t.Fatalf("outbox_lag_seconds points = %d, want 1", n)
	}
	app.RequireStop()
}

// Регрессия FX-12: схема таблицы инстанса приезжает в группу pgkit.migrations
// сама — pgkit.MigrateModule накатывает её без ручного списка источников.
func TestInstance_ProvidesMigrations(t *testing.T) {
	cfg := valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
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
				return valid(outboxkit.Config{BatchSize: 1, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
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

// instanceDeps — граф Instance: фабрика поверх repo, шина bus и конфиг batch.
func instanceDeps(repo outboxkit.Repository, bus natskit.StreamBus) fx.Option {
	return fx.Provide(
		func() outboxkit.RepositoryFactory { return &fakeFactory{repo: repo} },
		func() natskit.StreamBus { return bus },
		func() pgkit.Transactor { return &pgtest.Recorder{} },
		func() outboxkit.Config {
			return valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
		},
	)
}

// Subject'ы инстанса старт сверяет со стримами, а Publish вне них отвергает: иначе строки
// копились бы без PubAck при здоровом сервисе.
func TestInstance_WithSubjectsChecksStreamsOnStart(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{}
	bus := &checkingBus{}
	var pub outboxkit.Publisher
	app := fxtest.New(t,
		instanceDeps(repo, bus),
		scheduler(),
		outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("order.>", "invoice.*")),
		fx.Invoke(fx.Annotate(func(p outboxkit.Publisher) { pub = p }, fx.ParamTags(`name:"orders"`))),
	)
	app.RequireStart()
	defer app.RequireStop()

	if want := [][]string{{"order.>", "invoice.*"}}; !slices.EqualFunc(bus.checked, want, slices.Equal[[]string]) {
		t.Fatalf("checked = %v, want %v", bus.checked, want)
	}
	ctx := pgkit.WithTestTx(context.Background())
	for _, subject := range []string{"order.created", "order.item.added", "invoice.paid"} {
		if err := pub.Publish(ctx, subject, struct{}{}); err != nil {
			t.Fatalf("Publish(%s): %v", subject, err)
		}
	}
	for _, subject := range []string{"user.created", "invoice.paid.late", "order"} {
		if err := pub.Publish(ctx, subject, struct{}{}); !errors.Is(err, outboxkit.ErrSubjectNotDeclared) {
			t.Fatalf("Publish(%s): err = %v, want ErrSubjectNotDeclared", subject, err)
		}
	}
	if len(repo.inserted) != 3 {
		t.Fatalf("inserted = %d, want 3", len(repo.inserted))
	}
	attrs := []string{"instance", "orders", "status", "undeclared_subject"}
	if got := collect().Value("outbox_events_inserted_total", attrs...); got != 3 {
		t.Fatalf("undeclared_subject counter = %v, want 3", got)
	}
}

// Выключенный JetStream и subject'ы без стрима — ошибка старта с именем инстанса, а не
// тики, которые падают каждую секунду, пока события копятся в таблице.
func TestOutbox_StreamsCheckedOnStart(t *testing.T) {
	disabled, err := natskit.NewStreamBus(natskit.Config{}, nil, nil)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	noStream := &checkingBus{err: fmt.Errorf("%w: order.>", natskit.ErrNoStream)}
	cases := map[string]struct {
		graph fx.Option
		want  error
		name  string
	}{
		"module, JetStream disabled": {
			fx.Options(deps(&fakeRepo{}, disabled, valid(outboxkit.Config{BatchSize: 10, Interval: time.Second})), outboxkit.Module),
			natskit.ErrJetStreamDisabled, outboxkit.DefaultInstance,
		},
		"instance, JetStream disabled": {
			fx.Options(instanceDeps(&fakeRepo{}, disabled), outboxkit.Instance("orders", "outbox_orders")),
			natskit.ErrJetStreamDisabled, "orders",
		},
		"instance, no stream": {
			fx.Options(instanceDeps(&fakeRepo{}, noStream), outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("order.>"))),
			natskit.ErrNoStream, "orders",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app := fx.New(fx.NopLogger, tc.graph, scheduler())
			err := app.Start(context.Background())
			defer func() { _ = app.Stop(context.Background()) }()
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "instance "+tc.name) {
				t.Fatalf("start err = %v, want %v naming instance %s", err, tc.want, tc.name)
			}
		})
	}
}

// Сервер NATS без JetStream при NATS_JETSTREAM_ENABLED=true: старт падает, а публикация — сбой
// брокера, не попытка строки, иначе очередь ушла бы в dead за бюджет попыток.
func TestOutbox_ServerWithoutJetStream(t *testing.T) {
	fix := testkit.StartNATS(t)
	t.Setenv("NATS_URL", fix.URL)
	t.Setenv("NATS_JETSTREAM_ENABLED", "true")
	ncfg, err := natskit.NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := natskit.New("zz", ncfg, logkit.Nop())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	app := fx.New(fx.NopLogger, deps(&fakeRepo{}, client.Stream(), valid(outboxkit.Config{BatchSize: 10, Interval: time.Second})),
		scheduler(), outboxkit.Module)
	if err := app.Start(ctx); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("start err = %v, want ErrJetStreamDisabled", err)
	}
	_ = app.Stop(ctx)

	for _, mode := range []outboxkit.PublishMode{outboxkit.PublishBatch, outboxkit.PublishOrdered} {
		repo := &fakeRepo{unpublished: []outboxkit.Message{msg(1, "orders.created")}}
		w, _ := newWorker(t, repo, outboxkit.NewStreamTransport(client.Stream()),
			outboxkit.Config{BatchSize: 10, PublishMode: mode, MaxAttempts: 1})
		if err := w.Run(ctx); !errors.Is(err, outboxkit.ErrUnavailable) {
			t.Fatalf("%s: tick err = %v, want ErrUnavailable", mode, err)
		}
		if fs := repo.recordedFailures(); len(fs) != 0 {
			t.Fatalf("%s: failures = %+v, want none: JetStream off is a broker outage", mode, fs)
		}
	}
}

func TestInstance_EmptySubjectRejected(t *testing.T) {
	err := fx.New(fx.NopLogger, outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("order.>", ""))).Err()
	if !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

// Тик не короче таймаута простоя транзакции пула — ошибка сборки графа: Postgres оборвал бы
// сессию, пока воркер ждёт PubAck. Без PoolConfig в графе или с нулём таймаута проверки нет.
func TestModule_TickTimeoutBelowIdleInTx(t *testing.T) {
	cfg := valid(outboxkit.Config{TickTimeout: time.Minute})
	graphs := map[string]fx.Option{
		"Module": fx.Options(deps(&fakeRepo{}, &capturingBus{}, cfg), scheduler(), outboxkit.Module),
		"Instance": fx.Options(
			fx.Provide(
				func() outboxkit.RepositoryFactory { return &fakeFactory{repo: &fakeRepo{}} },
				func() natskit.StreamBus { return &capturingBus{} },
				func() pgkit.Transactor { return &pgtest.Recorder{} },
				func() outboxkit.Config { return cfg },
			),
			scheduler(),
			outboxkit.Instance("orders", "outbox_orders"),
		),
		// Тик сверяется по конфигу инстанса, а не графа: override дольше общего конфига.
		"Instance override": fx.Options(
			fx.Provide(
				func() outboxkit.RepositoryFactory { return &fakeFactory{repo: &fakeRepo{}} },
				func() natskit.StreamBus { return &capturingBus{} },
				func() pgkit.Transactor { return &pgtest.Recorder{} },
				func() outboxkit.Config { return valid(outboxkit.Config{TickTimeout: time.Second}) },
			),
			scheduler(),
			outboxkit.Instance("orders", "outbox_orders", outboxkit.WithInstanceConfig(cfg)),
		),
	}
	for name, graph := range graphs {
		if err := fx.New(fx.NopLogger, graph).Err(); err != nil {
			t.Errorf("%s without PoolConfig: %v", name, err)
		}
		for idle, wantErr := range map[time.Duration]bool{time.Minute: true, 2 * time.Minute: false, 0: false} {
			err := fx.New(fx.NopLogger, graph, fx.Supply(pgkit.PoolConfig{IdleInTxSessionTimeout: idle})).Err()
			if errors.Is(err, outboxkit.ErrConfig) != wantErr {
				t.Errorf("%s, idle %s: err = %v, want ErrConfig %v", name, idle, err, wantErr)
			}
		}
	}
}

func TestInstance_ConfigOverride(t *testing.T) {
	repo := &fakeRepo{}
	factory := &fakeFactory{repo: repo}
	override := valid(outboxkit.Config{BatchSize: 7, Interval: 5 * time.Second, PublishMode: outboxkit.PublishOrdered})

	var ob *outboxkit.Outbox
	app := fxtest.New(t,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return factory },
			func() natskit.StreamBus { return &capturingBus{} },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config {
				return valid(outboxkit.Config{BatchSize: 100, Interval: time.Second, PublishMode: outboxkit.PublishBatch})
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

// WithMetricSubject: label subject метрик и backlog-gauge'ей инстанса — шаблон, а не subject с id,
// тот же шаблон и контекст трассы строки уходят в сигналы natskit (KO11, KO13, KO14, KO21).
func TestInstance_WithMetricSubject(t *testing.T) {
	collect := metricsSince(t)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{7}, SpanID: trace.SpanID{8}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	traced := msg(1, "order.1.created")
	traced.TraceParent = sc
	repo := &fakeRepo{
		unpublished: []outboxkit.Message{traced, msg(2, "order.2.created")},
		backlog: []outboxkit.SubjectBacklog{
			{Subject: "order.1.created", Oldest: 3 * time.Second, Pending: 1},
			{Subject: "order.2.created", Oldest: time.Second, Pending: 2},
		},
	}
	bus := &capturingBus{}
	fold := func(subject string) string {
		parts := strings.Split(subject, ".")
		parts[1] = "*"
		return strings.Join(parts, ".")
	}
	var ob *outboxkit.Outbox
	app := fxtest.New(t,
		fx.Provide(
			func() outboxkit.RepositoryFactory { return &fakeFactory{repo: repo} },
			func() natskit.StreamBus { return bus },
			func() pgkit.Transactor { return &pgtest.Recorder{} },
			func() outboxkit.Config {
				return valid(outboxkit.Config{BatchSize: 10, Interval: time.Hour, PublishMode: outboxkit.PublishBatch})
			},
		),
		scheduler(),
		outboxkit.Instance("orders", "outbox_orders", outboxkit.WithMetricSubject(fold)),
		fx.Invoke(fx.Annotate(func(o *outboxkit.Outbox) { ob = o }, fx.ParamTags(`name:"orders"`))),
	)
	app.RequireStart()
	defer app.RequireStop()

	if err := ob.Publisher().Publish(context.Background(), "order.3.created", map[string]int{"id": 3}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := ob.Worker().Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rm := collect()
	tpl := []string{"instance", "orders", "subject", "order.*.created"}
	if got := rm.Value("outbox_events_inserted_total", append(tpl, "status", "ok")...); got != 1 {
		t.Fatalf("inserted by template = %v, want 1", got)
	}
	if got := rm.Value("outbox_events_processed_total", append(tpl, "status", "published")...); got != 2 {
		t.Fatalf("processed by template = %v, want 2", got)
	}
	if got := rm.Value("outbox_lag_seconds", tpl...); got != 3 {
		t.Fatalf("lag by template = %v, want 3", got)
	}
	if got := rm.Value("outbox_pending_count", tpl...); got != 3 {
		t.Fatalf("pending by template = %v, want 3", got)
	}
	msgs := bus.msgs()
	if len(msgs) != 2 || msgs[0].MetricSubject != "order.*.created" || !msgs[0].TraceParent.Equal(sc) || msgs[1].TraceParent.IsValid() {
		t.Fatalf("published %+v, want the template and the trace context of the first row only", msgs)
	}
}

func TestOutbox_ReviveDead(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{revived: 3}
	o := mustNew(t, "orders", repo, &fakeTransport{}, &pgtest.Recorder{},
		valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}), logkit.Nop())

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
	if got := collect().Value("outbox_revived_total", "instance", "orders"); got != 3 {
		t.Fatalf("outbox_revived_total = %v, want 3", got)
	}
}

func TestOutbox_ReviveDeadUnsupported(t *testing.T) {
	o := mustNew(t, "orders", bareRepo{repo: &fakeRepo{}}, &fakeTransport{}, &pgtest.Recorder{},
		valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}), logkit.Nop())

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
	o := mustNew(t, "orders", repo, &fakeTransport{}, &pgtest.Recorder{},
		valid(outboxkit.Config{BatchSize: 10, Interval: time.Second, PublishMode: outboxkit.PublishBatch}), logkit.Nop())

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
		outboxkit.Instance("orders", "outbox_orders", outboxkit.WithSubjects("orders.>")),
		fx.Invoke(fx.Annotate(func(outboxkit.Publisher) {}, fx.ParamTags(`name:"orders"`))),
	)
	if err != nil {
		t.Fatalf("multi-instance recipe does not resolve: %v", err)
	}
}
