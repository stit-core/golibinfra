package outboxkit

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — orchestration-слой инстанса DefaultInstance; Repository,
// pgkit.Transactor, logkit.Logger и Config (`fx.Provide(outboxkit.NewConfig)`) провайдит
// caller. Выключенный JetStream — ошибка старта.
var Module = fx.Module(
	"outboxkit",
	fx.Provide(
		NewStreamTransport,
		func(repo Repository, tr Transport, tx pgkit.Transactor, cfg Config, log logkit.Logger) *Outbox {
			return New(DefaultInstance, repo, tr, tx, cfg, log)
		},
		func(o *Outbox) *Bus { return o.bus },
		func(o *Outbox) Publisher { return o.Publisher() },
		func(o *Outbox) *Worker { return o.Worker() },
	),
	timerkit.Provide(func(o *Outbox) timerkit.Job { return o.Job() }),
	fx.Invoke(registerInstanceGauges),
	fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus) { checkStreams(lc, sb, DefaultInstance, nil) }),
)
