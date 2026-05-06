package bruteforcekit

import "go.uber.org/fx"

var Module = fx.Module("bruteforcekit",
	fx.Provide(NewConfig, NewPgGuard),
)
