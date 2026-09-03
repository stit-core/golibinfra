package authnkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Argon2idParams и JWTConfig — caller регистрирует их сам
// (через App-структуру или `fx.Provide(authnkit.NewArgon2idParams, authnkit.NewJWTConfig)`).
var Module = fx.Module("authnkit",
	fx.Provide(
		fx.Annotate(NewArgon2idHasher, fx.As(new(Hasher))),
		NewHS256JWT,
		func(j *HS256JWT) JWTSigner { return j },
		func(j *HS256JWT) JWTParser { return j },
		NewRegistry,
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

func NewTOTPConfig() (TOTPConfig, error) {
	var c TOTPConfig
	return c, envconfig.Process("", &c)
}
