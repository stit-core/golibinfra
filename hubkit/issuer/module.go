package issuer

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module — контур выпуска hub-сессии для employee-backend: Signer (ES256 +
// ротация), RefreshManager (rotating refresh поверх Redis) и роут JWKS.
//
// Module не provide'ит KeyConfig/CookieConfig/RefreshConfig (asymmetry-нота как в
// pgkit — caller добавляет `fx.Provide(issuer.NewKeyConfig, issuer.NewCookieConfig,
// issuer.NewRefreshConfig)`). RefreshStore — внешняя зависимость: caller
// подключает `fx.Provide(issuer.NewCacheRefreshStore)` поверх cachekit.Cache
// (или NewMemoryRefreshStore). Cookie-выпуск и login/refresh/logout-роуты живут
// в самом employee-backend (продуктовая специфика), тут — только примитивы.
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
