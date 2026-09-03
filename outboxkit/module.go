package outboxkit

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — orchestration-слой инстанса DefaultInstance; Repository,
// pgkit.Transactor и Config (`fx.Provide(outboxkit.NewConfig)`) провайдит caller.
var Module = fx.Module(
	"outboxkit",
	fx.Provide(
		func(repo Repository) *Bus { return NewBus(DefaultInstance, repo) },
		func(b *Bus) Publisher { return b },
		NewStreamTransport,
		func(repo Repository, tr Transport, tx pgkit.Transactor, cfg Config) *Worker {
			return NewWorker(DefaultInstance, repo, tr, tx, cfg)
		},
		fx.Annotate(
			func(w *Worker, cfg Config) timerkit.Job {
				return timerkit.Job{
					Name:     "outboxkit.publisher",
					Interval: cfg.Interval,
					Run:      w.Run,
				}
			},
			fx.ResultTags(`group:"timer.job"`),
		),
	),
	fx.Invoke(registerBacklogGauges),
)

// registerBacklogGauges подключает `outbox_lag_seconds`/`outbox_pending_count`,
// если репозиторий умеет BacklogReporter (pgstore.Store умеет); иначе no-op.
func registerBacklogGauges(lc fx.Lifecycle, repo Repository) error {
	reporter, ok := repo.(BacklogReporter)
	if !ok {
		return nil
	}
	reg, err := RegisterBacklogGauges(DefaultInstance, reporter)
	if err != nil {
		return err
	}
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error { return reg.Unregister() },
	})
	return nil
}
