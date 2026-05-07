package clockkit

import "go.uber.org/fx"

var Module = fx.Module("clockkit",
	fx.Provide(func() Clock { return Real{} }),
)
