package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

var Module = fx.Module(
	"bruteforcekit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(bruteforcekit.Guard))),
	),
	pgkit.ProvideMigrations(Migrations),
)
