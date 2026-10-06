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
// caller. Выключенный JetStream и тик не короче PG_IDLE_IN_TX_SESSION_TIMEOUT пула (если
// pgkit.PoolConfig в графе) — ошибка старта.
var Module = fx.Module(
	"outboxkit",
	fx.Provide(
		NewStreamTransport,
		func(in moduleIn) (*Outbox, error) {
			if err := checkIdleInTx(in.Cfg, in.Pool); err != nil {
				return nil, err
			}
			return New(DefaultInstance, in.Repo, in.Tr, in.Tx, in.Cfg, in.Log)
		},
		func(o *Outbox) *Bus { return o.bus },
		func(o *Outbox) Publisher { return o.Publisher() },
		func(o *Outbox) *Worker { return o.Worker() },
	),
	timerkit.Provide(func(o *Outbox) timerkit.Job { return o.Job() }),
	fx.Invoke(registerInstanceGauges),
	fx.Invoke(func(lc fx.Lifecycle, sb natskit.StreamBus) { checkStreams(lc, sb, DefaultInstance, nil) }),
)

// moduleIn: конфиг пула опционален — с ним тик сверяется с таймаутом простоя транзакции.
type moduleIn struct {
	fx.In

	Repo Repository
	Tr   Transport
	Tx   pgkit.Transactor
	Cfg  Config
	Log  logkit.Logger
	Pool pgkit.PoolConfig `optional:"true"`
}
