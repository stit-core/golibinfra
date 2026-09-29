package outboxkit

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/fx"

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

// New собирает именованный outbox поверх готовых repo/transport/transactor.
func New(name string, repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Outbox {
	instr := newOutboxInstruments(name)
	return &Outbox{
		name:   name,
		repo:   repo,
		bus:    newBus(instr, repo),
		worker: newWorker(instr, repo, tr, tx, cfg),
		instr:  instr,
		cfg:    cfg,
	}
}

// Name — имя инстанса; значение label `instance` в метриках.
func (o *Outbox) Name() string { return o.name }

// Publisher — insert-сторона инстанса для доменного кода.
func (o *Outbox) Publisher() Publisher { return o.bus }

// Worker — drain-сторона инстанса (обычно нужна только через Job).
func (o *Outbox) Worker() *Worker { return o.worker }

// Job — периодический drain инстанса для fx-группы timerkit.Group.
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
	cfg *Config
}

// InstanceOption — опция сборки Instance.
type InstanceOption func(*instanceOptions)

// WithInstanceConfig задаёт конфиг инстанса вместо общего outboxkit.Config из
// графа — например ordered-режим для одного домена из нескольких.
func WithInstanceConfig(cfg Config) InstanceOption {
	return func(o *instanceOptions) { o.cfg = &cfg }
}

// Instance собирает именованный outbox поверх таблицы table: store и схему
// таблицы (в группу pgkit.MigrationsGroup) из RepositoryFactory, namespace'ный
// транспорт, drain- и maintenance-job'ы в группе timerkit.Group, backlog-gauge'и.
// *Outbox и Publisher — под fx-тегом `name:"<name>"`; рецепт — docs/usage.md.
func Instance(name, table string, opts ...InstanceOption) fx.Option {
	if name == "" {
		return fx.Error(fmt.Errorf("%w: outbox instance name is empty", ErrConfig))
	}
	var o instanceOptions
	for _, opt := range opts {
		opt(&o)
	}
	tag := `name:"` + name + `"`
	return fx.Module(
		"outboxkit:"+name,
		fx.Provide(
			fx.Annotate(
				func(f RepositoryFactory, sb natskit.StreamBus, tx pgkit.Transactor, cfg Config) (*Outbox, error) {
					repo, err := f.Repository(table)
					if err != nil {
						return nil, err
					}
					if o.cfg != nil {
						cfg = *o.cfg
					}
					return New(name, repo, NewStreamTransportFor(sb, name), tx, cfg), nil
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
		timerkit.Provide(
			func(ob *Outbox, _ pgkit.Migrated) []timerkit.Job { return ob.Jobs() },
			fx.ParamTags(tag, `optional:"true"`),
		),
		fx.Invoke(fx.Annotate(registerInstanceGauges, fx.ParamTags(``, tag))),
	)
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
