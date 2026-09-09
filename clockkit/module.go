package clockkit

import "go.uber.org/fx"

// Module провайдит Clock как Real{}. Фейки во fx-граф не отдаются — их
// подставляют напрямую в тестах.
var Module = fx.Module(
	"clockkit",
	fx.Provide(func() Clock { return Real{} }),
)
