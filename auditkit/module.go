package auditkit

import "go.uber.org/fx"

var Module = fx.Module("auditkit",
	fx.Provide(NewPgLogger),
)
