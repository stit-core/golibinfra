package outboxkit

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module провайдит orchestration-слой outbox (Bus, Publisher, Worker, drain-Job).
// Repository — внешняя зависимость; caller подключает отдельно: либо
// `outboxkit/pgstore.Module`, либо собственный store-адаптер.
// Конфиг тоже не provide'ится — caller добавляет `fx.Provide(outboxkit.NewConfig)`.
// Worker публикует в JetStream через natskit.StreamBus — требует
// NATS_JETSTREAM_ENABLED=true и стрим, покрывающий subject'ы outbox'а.
var Module = fx.Module(
	"outboxkit",
	fx.Provide(
		NewBus,
		func(b *Bus) Publisher { return b },
		NewStreamTransport,
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
	fx.Invoke(registerBacklogGaugesIfReporter),
)

// backlogParams — optional BacklogReporter: подключается только если репозиторий
// его реализует (см. outboxkit/pgstore.Store). Без него — no-op,
// `outbox_lag_seconds`/`outbox_pending_count` просто не эмитятся.
type backlogParams struct {
	fx.In
	LC       fx.Lifecycle
	Reporter BacklogReporter `optional:"true"`
}

func registerBacklogGaugesIfReporter(p backlogParams) error {
	if p.Reporter == nil {
		return nil
	}
	reg, err := RegisterBacklogGauges(p.Reporter)
	if err != nil {
		return err
	}
	p.LC.Append(fx.Hook{
		OnStop: func(_ context.Context) error { return reg.Unregister() },
	})
	return nil
}
