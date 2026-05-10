package outboxkit

import (
	"go.uber.org/fx"

	"git.stit.tech/stit/golibinfra/timerkit"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(outboxkit.NewConfig)`.
var Module = fx.Module("outboxkit",
	fx.Provide(
		NewRepository,
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
