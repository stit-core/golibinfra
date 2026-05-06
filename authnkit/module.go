package authnkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("authnkit",
	fx.Provide(
		newArgon2idParams,
		fx.Annotate(NewArgon2idHasher, fx.As(new(Hasher))),
	),
)

func newArgon2idParams() (Argon2idParams, error) {
	var p Argon2idParams
	return p, envconfig.Process("", &p)
}
