package authnkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("authnkit",
	fx.Provide(
		newArgon2idParams,
		fx.Annotate(NewArgon2idHasher, fx.As(new(Hasher))),
		newJWTConfig,
		NewHS256JWT,
		fx.Annotate(NewHS256JWT, fx.As(new(JWTSigner))),
		fx.Annotate(NewHS256JWT, fx.As(new(JWTParser))),
	),
)

func newArgon2idParams() (Argon2idParams, error) {
	var p Argon2idParams
	return p, envconfig.Process("", &p)
}

func newJWTConfig() (JWTConfig, error) {
	var c JWTConfig
	return c, envconfig.Process("", &c)
}
