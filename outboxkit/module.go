package outboxkit

import (
	"go.uber.org/fx"

	"git.stit.tech/stit/golibinfra/timerkit"
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
)
