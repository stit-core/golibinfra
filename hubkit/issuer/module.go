package issuer

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module — контур выпуска hub-сессии: Signer (ES256 + ротация), RefreshManager
// (rotating refresh поверх cachekit.Cache) и роут JWKS.
//
// Module не provide'ит KeyConfig/CookieConfig/RefreshConfig (asymmetry-нота как в
// pgkit — caller добавляет `fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig,
// issuer.NewRefreshConfig)`) и cachekit.Cache (cachekit.Module или
// cachekit.NewMemoryCache для single-replica без Redis). Cookie-выпуск и
// login/refresh/logout-роуты живут в самом issuer-сервисе, тут — только примитивы.
var Module = fx.Module(
	"hubkit-issuer",
	fx.Provide(
		NewSigner,
		NewRefreshManager,
	),
	fx.Invoke(RegisterJWKS),
)

func NewKeyConfig() (KeyConfig, error) {
	var c KeyConfig
	return c, envconfig.Process("", &c)
}

func NewCookieConfig() (CookieConfig, error) {
	var c CookieConfig
	return c, envconfig.Process("", &c)
}

func NewRefreshConfig() (RefreshConfig, error) {
	var c RefreshConfig
	return c, envconfig.Process("", &c)
}
