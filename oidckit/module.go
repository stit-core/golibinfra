package oidckit

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// SecuritySchemeName — имя openapi security-scheme cookie-сессии; операции
// защищаются `Security: []map[string][]string{{oidckit.SecuritySchemeName: {}}}`.
const SecuritySchemeName = "Cookie"

// NewSecurityScheme — запись для `components.securitySchemes` (apiKey in cookie).
// Module кладёт её в group `httpkit.security_scheme` сам; без fx — передай в
// httpkit.NewServer.
func NewSecurityScheme(cookie CookieConfig) httpkit.SecuritySchemeEntry {
	return httpkit.SecuritySchemeEntry{
		Name: SecuritySchemeName,
		Scheme: &huma.SecurityScheme{
			Type:        "apiKey",
			In:          "cookie",
			Name:        cookie.SessionCookieName(),
			Description: "httpOnly cookie set by the OIDC callback and sent by the browser automatically.",
		},
	}
}

// NewConfig читает OIDC_* из окружения и валидирует их (ErrConfig).
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// NewCookieConfig читает COOKIE_* из окружения и валидирует их (ErrConfig).
func NewCookieConfig() (CookieConfig, error) {
	var c CookieConfig
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

type bffParams struct {
	fx.In

	Config Config
	// HTTP обязателен: походы в idp идут через httpclient (retry, breaker на
	// хост, OTel-спаны, корпоративный CA) — своего клиента kit не заводит.
	HTTP   *httpclient.Client
	Signer authnkit.JWTSigner
	Store  StateStore
	Cookie CookieConfig
	Hooks  Hooks `optional:"true"`
	Log    logkit.Logger
}

func newBFF(p bffParams) (*BFF, error) {
	return NewBFF(p.Config, p.HTTP, p.Signer, p.Store, p.Cookie, p.Hooks, p.Log)
}

// warmup требует успешного discovery на старте, если OIDC_DISCOVERY_ON_START=true.
func warmup(lc fx.Lifecycle, b *BFF, cfg Config) {
	if !cfg.DiscoveryOnStart {
		return
	}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error { return b.Warmup(ctx) }})
}

// Module — drop-in BFF: роуты login/callback/me/logout, security-scheme Cookie,
// httpkit.AuthExtractor (cookie → Identity в ctx), health-проба и user_id в логах.
// Требования к графу и ограничения — README кита (§ Быстрый старт).
var Module = fx.Module(
	"oidckit",
	fx.Provide(
		newBFF,
		NewExtractor,

		func(e *Extractor) httpkit.AuthExtractor { return e.Extract },
		fx.Annotate(NewSecurityScheme, fx.ResultTags(`group:"httpkit.security_scheme"`)),
	),
	healthkit.Provide(func(b *BFF) healthkit.Probe { return b.Probe() }),
	fx.Invoke(RegisterIdentityLogAttr),
	fx.Invoke(warmup),
	fx.Invoke(RegisterRoutes),
)
