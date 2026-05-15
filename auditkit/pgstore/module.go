package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-infra/golibinfra/auditkit"
)

var Module = fx.Module(
	"auditkit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(auditkit.Logger))),
	),
)
