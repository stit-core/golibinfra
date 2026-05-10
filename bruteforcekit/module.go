package bruteforcekit

import "go.uber.org/fx"

// Module не provide'ит Config — caller обязан зарегистрировать его сам
// (типично через App-структуру). Если приложение не использует App-pattern,
// добавь `fx.Provide(bruteforcekit.NewConfig)`.
var Module = fx.Module("bruteforcekit",
	fx.Provide(NewPgGuard),
)
