package pgstore

import (
	"go.uber.org/fx"

	"git.sftbmb.ru/sftbmb-core/golibinfra/auditkit"
)

var Module = fx.Module(
	"auditkit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(auditkit.Logger))),
	),
)
