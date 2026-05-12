package pgstore

import (
	"go.uber.org/fx"

	"git.stit.tech/stit/golibinfra/bruteforcekit"
)

var Module = fx.Module(
	"bruteforcekit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(bruteforcekit.Guard))),
	),
)
