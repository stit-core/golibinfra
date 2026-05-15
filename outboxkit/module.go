package outboxkit

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-infra/golibinfra/timerkit"
)

// Module провайдит orchestration-слой outbox (Bus, Publisher, Worker, drain-Job).
// Repository — внешняя зависимость; caller подключает отдельно: либо
// `outboxkit/pgstore.Module`, либо собственный store-адаптер.
// Конфиг тоже не provide'ится — caller добавляет `fx.Provide(outboxkit.NewConfig)`.
var Module = fx.Module(
	"outboxkit",
	fx.Provide(
		NewBus,
		func(b *Bus) Publisher { return b },
		NewWorker,
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
	fx.Invoke(registerLagGaugeIfReporter),
)

// lagParams — optional LagReporter: подключается только если репозиторий
// его реализует (см. outboxkit/pgstore.Store). Без него — no-op,
// `outbox_lag_seconds` просто не эмитится.
type lagParams struct {
	fx.In
	LC       fx.Lifecycle
	Reporter LagReporter `optional:"true"`
}

func registerLagGaugeIfReporter(p lagParams) error {
	if p.Reporter == nil {
		return nil
	}
	reg, err := RegisterLagGauge(p.Reporter)
	if err != nil {
		return err
	}
	p.LC.Append(fx.Hook{
		OnStop: func(_ context.Context) error { return reg.Unregister() },
	})
	return nil
}

