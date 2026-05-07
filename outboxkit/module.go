package outboxkit

import (
	"go.uber.org/fx"

	"git.stit.tech/akadmin/golibinfra/timerkit"
)

var Module = fx.Module("outboxkit",
	fx.Provide(
		NewConfig,
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
