package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

var Module = fx.Module(
	"auditkit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(auditkit.Logger))),
	),
	pgkit.ProvideMigrations(Migrations),
)
