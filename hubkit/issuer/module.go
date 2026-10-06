package issuer

import (
	"fmt"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module — контур выпуска hub-сессии: Signer (ES256 + ротация), RefreshManager
// (rotating refresh поверх cachekit.Cache) и роут JWKS.
//
// Module не provide'ит KeyConfig/CookieConfig/RefreshConfig (caller добавляет
// `fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig, issuer.NewRefreshConfig)`)
// и cachekit.Cache (cachekit.Module или cachekit.NewMemoryCache для
// single-replica, state — в процессе). huma.API в графе опционален: с ним
// Module вешает JWKS-роут сам, без него сервис монтирует JWKSHandler. Cookie-выпуск
// и login/refresh/logout-роуты живут в самом issuer-сервисе, тут — только примитивы.
var Module = fx.Module(
	"hubkit/issuer",
	fx.Provide(
		NewSigner,
		NewRefreshManager,
	),
	fx.Invoke(registerJWKS),
)

type jwksParams struct {
	fx.In

	Signer *Signer
	API    huma.API `optional:"true"`
}

func registerJWKS(p jwksParams) {
	if p.API == nil {
		return
	}
	RegisterJWKS(p.API, p.Signer)
}

// NewKeyConfig читает HUB_SESSION_* ключей из ENV и валидирует их.
func NewKeyConfig() (KeyConfig, error) {
	var c KeyConfig
	if err := envconf.Load(&c); err != nil {
		return KeyConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// NewCookieConfig читает cookie-слой из ENV и валидирует его.
func NewCookieConfig() (CookieConfig, error) {
	var c CookieConfig
	if err := envconf.Load(&c); err != nil {
		return CookieConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// NewRefreshConfig читает HUB_REFRESH_* из ENV и валидирует их.
func NewRefreshConfig() (RefreshConfig, error) {
	var c RefreshConfig
	if err := envconf.Load(&c); err != nil {
		return RefreshConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
