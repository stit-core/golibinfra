package authnkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Argon2idParams и JWTConfig — caller обязан
// зарегистрировать их сам (типично через App-структуру). Однородно с
// httpkit/pgkit. Если приложение не использует App-pattern,
// добавь `fx.Provide(authnkit.NewArgon2idParams, authnkit.NewJWTConfig)`.
var Module = fx.Module("authnkit",
	fx.Provide(
		fx.Annotate(NewArgon2idHasher, fx.As(new(Hasher))),
		NewHS256JWT,
		fx.Annotate(NewHS256JWT, fx.As(new(JWTSigner))),
		fx.Annotate(NewHS256JWT, fx.As(new(JWTParser))),
	),
)

func NewArgon2idParams() (Argon2idParams, error) {
	var p Argon2idParams
	return p, envconfig.Process("", &p)
}

func NewJWTConfig() (JWTConfig, error) {
	var c JWTConfig
	return c, envconfig.Process("", &c)
}
