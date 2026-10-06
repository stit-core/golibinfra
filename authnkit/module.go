package authnkit

import (
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module не provide'ит Argon2idConfig, JWTConfig и TOTPConfig — caller
// регистрирует их сам (через App-структуру или `fx.Provide(authnkit.NewArgon2idConfig,
// authnkit.NewJWTConfig, authnkit.NewTOTPConfig)`).
var Module = fx.Module("authnkit",
	fx.Provide(
		fx.Annotate(newHasher, fx.As(fx.Self()), fx.As(new(Hasher))),
		fx.Annotate(newJWT, fx.As(fx.Self()), fx.As(new(JWTSigner)), fx.As(new(JWTParser))),
		newTOTPGenerator,
		NewRegistry,
	),
)

// Конструкторы кита вариадические (opts ...Option), поэтому в графе они живут
// обёртками: fx.Annotate вариадические функции не аннотирует.
func newHasher(p Argon2idConfig) (*Argon2idHasher, error) { return NewArgon2idHasher(p) }

func newJWT(cfg JWTConfig) (*HS256JWT, error) { return NewHS256JWT(cfg) }

func newTOTPGenerator(cfg TOTPConfig) (*TOTPGenerator, error) { return NewTOTP(cfg) }

// NewArgon2idConfig читает ARGON2_* из ENV и валидирует их.
func NewArgon2idConfig() (Argon2idConfig, error) {
	var p Argon2idConfig
	if err := envconf.Load(&p); err != nil {
		return Argon2idConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return p, p.Validate()
}

// NewJWTConfig читает JWT_* из ENV и валидирует их.
func NewJWTConfig() (JWTConfig, error) {
	var c JWTConfig
	if err := envconf.Load(&c); err != nil {
		return JWTConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// NewTOTPConfig читает TOTP_* из ENV и валидирует их.
func NewTOTPConfig() (TOTPConfig, error) {
	var c TOTPConfig
	if err := envconf.Load(&c); err != nil {
		return TOTPConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
