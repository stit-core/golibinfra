package outboxkit

import (
	"context"
	"fmt"
	"slices"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Outbox — один независимый transactional-outbox: своя таблица (через repo),
// конфиг, Bus и Worker. Приложение собирает несколько Outbox'ов — по одному на
// домен — через Instance или вручную через New.
type Outbox struct {
	name   string
	repo   Repository
	bus    *Bus
	worker *Worker
	instr  *outboxInstruments
	cfg    Config
}

// New собирает именованный outbox поверх готовых repo/transport/transactor; в log Worker
// пишет ERROR на каждый переход строки в dead (nil — logkit.Nop()). Невалидный cfg — ErrConfig.
func New(name string, cfg Config, repo Repository, tr Transport, tx pgkit.Transactor, log logkit.Logger) (*Outbox, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	instr := newOutboxInstruments(name)
	return &Outbox{
		name:   name,
		repo:   repo,
		bus:    newBus(instr, repo, cfg.MaxPayloadBytes),
		worker: newWorker(name, instr, repo, tr, tx, cfg, log),
		instr:  instr,
		cfg:    cfg,
	}, nil
}

// Name — имя инстанса; значение label `instance` в метриках.
func (o *Outbox) Name() string { return o.name }

// Publisher — insert-сторона инстанса для доменного кода.
func (o *Outbox) Publisher() Publisher { return o.bus }

// Worker — drain-сторона инстанса (обычно нужна только через Job).
func (o *Outbox) Worker() *Worker { return o.worker }

// Job — периодический drain инстанса для fx-группы timerkit.JobsGroup.
func (o *Outbox) Job() timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.publisher." + o.name,
		Interval: o.cfg.Interval,
		Run:      o.worker.Run,
	}
}

// Jobs — drain инстанса плюс job'ы обслуживания хранилища, если репозиторий
// реализует Maintainer (pgstore.Store — партиции и retention).
func (o *Outbox) Jobs() []timerkit.Job {
	jobs := []timerkit.Job{o.Job()}
	if m, ok := o.repo.(Maintainer); ok {
		jobs = append(jobs, m.MaintenanceJobs()...)
	}
	return jobs
}

// RegisterBacklogGauges подключает `outbox_lag_seconds`/`outbox_pending_count`
// инстанса. Хранилище без BacklogReporter — не ошибка: возвращается no-op
// Registration, так что `defer reg.Unregister()` безопасен всегда.
func (o *Outbox) RegisterBacklogGauges() (metric.Registration, error) {
	reporter, ok := o.repo.(BacklogReporter)
	if !ok {
		return noop.Registration{}, nil
	}
	return RegisterBacklogGauges(o.name, reporter)
}

// ReviveDead возвращает dead-строки инстанса в очередь и считает их в
// `outbox_revived_total`. Хранилище без поддержки замены — ErrNotSupported.
func (o *Outbox) ReviveDead(ctx context.Context, filter ReviveFilter) (int64, error) {
	reviver, ok := o.repo.(DeadReviver)
	if !ok {
		return 0, fmt.Errorf("%w: ReviveDead", ErrNotSupported)
	}
	n, err := reviver.ReviveDead(ctx, filter)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		o.instr.revivedTotal.Add(ctx, n, metric.WithAttributes(o.instr.instance))
	}
	return n, nil
}

type instanceOptions struct {
	cfg      *Config
	subjects []string
}

// InstanceOption — опция сборки Instance.
type InstanceOption func(*instanceOptions)

// WithInstanceConfig задаёт конфиг инстанса вместо общего outboxkit.Config из
// графа — например ordered-режим для одного домена из нескольких.
func WithInstanceConfig(cfg Config) InstanceOption {
	return func(o *instanceOptions) { o.cfg = &cfg }
}

// WithSubjects объявляет subject'ы событий инстанса (фильтры NATS с `*`, `>`, например
// `user.>`): старт проверяет, что каждый целиком накрыт стримом JetStream, Publish отвергает
// остальные (ErrSubjectNotDeclared). Без опции старт проверяет только, что JetStream включён.
func WithSubjects(filters ...string) InstanceOption {
	return func(o *instanceOptions) { o.subjects = append(o.subjects, filters...) }
}

// Instance собирает именованный outbox поверх таблицы table: store и схему
// таблицы (в группу pgkit.MigrationsGroup) из RepositoryFactory, namespace'ный
// транспорт, drain- и maintenance-job'ы в группе timerkit.JobsGroup, backlog-gauge'и и
// проверку стримов на старте (checkStreams). *Outbox и Publisher — под fx-тегом
// `name:"<name>"`; рецепт — docs/usage.md.
func Instance(name, table string, opts ...InstanceOption) fx.Option {
	if name == "" {
		return fx.Error(fmt.Errorf("%w: outbox instance name is empty", ErrConfig))
	}
	var o instanceOptions
	for _, opt := range opts {
		opt(&o)
	}
	if slices.Contains(o.subjects, "") {
		return fx.Error(fmt.Errorf("%w: outbox instance %s: empty subject in WithSubjects", ErrConfig, name))
	}
	tag := `name:"` + name + `"`
	return fx.Module(
		"outboxkit:"+name,
		fx.Provide(
			fx.Annotate(
				func(in instanceIn) (*Outbox, error) {
					cfg := in.Cfg
					if o.cfg != nil {
						cfg = *o.cfg
					}
					if err := checkIdleInTx(cfg, in.Pool); err != nil {
						return nil, fmt.Errorf("outboxkit: instance %s: %w", name, err)
					}
					repo, err := in.Factory.Repository(table)
					if err != nil {
						return nil, err
					}
					ob, err := New(name, cfg, repo, NewStreamTransportFor(in.Stream, name), in.Tx, in.Log)
					if err != nil {
						return nil, fmt.Errorf("outboxkit: instance %s: %w", name, err)
					}
					ob.bus.subjects = o.subjects
					return ob, nil
				},
				fx.ResultTags(tag),
			),
			fx.Annotate(
				func(ob *Outbox) Publisher { return ob.Publisher() },
				fx.ParamTags(tag), fx.ResultTags(tag),
			),
			fx.Annotate(
				func(f RepositoryFactory) pgkit.Source { return f.Migrations(table) },
				fx.ResultTags(`group:"`+pgkit.MigrationsGroup+`"`),
			),
		),
		// Зависимость от pgkit.Migrated ставит job'ы обслуживания в очередь
		// после миграций: партиции создаются поверх уже существующей таблицы.
		timerkit.ProvideJob(
			func(ob *Outbox, _ pgkit.Migrated) []timerkit.Job { return ob.Jobs() },
			fx.ParamTags(tag, `optional:"true"`),
		),
		fx.Invoke(fx.Annotate(checkSchema, fx.ParamTags(``, tag, `optional:"true"`))),
		fx.Invoke(fx.Annotate(registerInstanceGauges, fx.ParamTags(``, tag))),
		fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus) { checkStreams(lc, sb, name, o.subjects) }),
	)
}

// instanceIn: конфиг пула опционален — с ним тик сверяется с таймаутом простоя транзакции.
type instanceIn struct {
	fx.In

	Factory RepositoryFactory
	Stream  natskit.StreamBus
	Tx      pgkit.Transactor
	Cfg     Config
	Log     logkit.Logger
	Pool    pgkit.PoolConfig `optional:"true"`
}

// checkStreams вешает проверку старта: JetStream включён, subject'ы WithSubjects накрыты
// стримом — иначе строки копились бы в таблице без PubAck при «здоровом» сервисе. Шина без
// natskit.StreamChecker (фейк natstest) стримов не знает — проверки нет.
func checkStreams(lc fx.Lifecycle, sb natskit.StreamBus, name string, subjects []string) {
	checker, ok := sb.(natskit.StreamChecker)
	if !ok {
		return
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := checker.CheckStreams(ctx, subjects...); err != nil {
			return fmt.Errorf("outboxkit: instance %s: %w", name, err)
		}
		return nil
	}})
}

// checkSchema вешает на старт проверку схемы хранилища (SchemaChecker); зависимость от
// pgkit.Migrated ставит её после миграций.
func checkSchema(lc fx.Lifecycle, o *Outbox, _ pgkit.Migrated) {
	checker, ok := o.repo.(SchemaChecker)
	if !ok {
		return
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := checker.CheckSchema(ctx); err != nil {
			return fmt.Errorf("outboxkit: instance %s: %w", o.name, err)
		}
		return nil
	}})
}

func registerInstanceGauges(lc fx.Lifecycle, o *Outbox) error {
	reg, err := o.RegisterBacklogGauges()
	if err != nil || reg == nil {
		return err
	}
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error { return reg.Unregister() },
	})
	return nil
}
