package pgstore

import (
	"go.uber.org/fx"

	"git.sftbmb.ru/sftbmb-core/golibinfra/bruteforcekit"
)

var Module = fx.Module(
	"bruteforcekit/pgstore",
	fx.Provide(
		fx.Annotate(NewStore, fx.As(new(bruteforcekit.Guard))),
	),
)
