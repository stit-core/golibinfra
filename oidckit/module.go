package oidckit

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// SecuritySchemeName — имя openapi security-scheme cookie-сессии; операции
// защищаются `Security: []map[string][]string{{oidckit.SecuritySchemeName: {}}}`.
const SecuritySchemeName = "Cookie"

// NewSecurityScheme — запись для `components.securitySchemes` (apiKey in cookie).
// Module кладёт её в group `httpkit.security_scheme` сам; без fx — передай в
// httpkit.NewServer.
func NewSecurityScheme() httpkit.SecuritySchemeEntry {
	return httpkit.SecuritySchemeEntry{
		Name: SecuritySchemeName,
		Scheme: &huma.SecurityScheme{
			Type:        "apiKey",
			In:          "cookie",
			Name:        CookieAccess,
			Description: "httpOnly cookie выставляется OIDC-callback'ом и автоматически прикладывается браузером.",
		},
	}
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func NewCookieConfig() (CookieConfig, error) {
	var c CookieConfig
	return c, envconfig.Process("", &c)
}

type bffParams struct {
	fx.In

	Config Config
	Signer authnkit.JWTSigner
	Store  StateStore
	Cookie CookieConfig
	Hooks  Hooks `optional:"true"`
	Log    logkit.Logger
}

func newBFF(p bffParams) *BFF {
	return NewBFF(p.Config, p.Signer, p.Store, p.Cookie, p.Hooks, p.Log)
}

// Module — drop-in BFF: роуты login/callback/me/logout, security-scheme Cookie,
// httpkit.AuthExtractor (cookie → Identity в ctx), user_id в логах.
//
// Module не provide'ит Config/CookieConfig (asymmetry note в pgkit/module.go —
// caller добавляет `fx.Provide(oidckit.NewConfig, oidckit.NewCookieConfig)`),
// StateStore (Redis: `fx.Provide(fx.Annotate(oidckit.NewCacheStateStore, ...))`
// поверх cachekit; либо NewMemoryStateStore) и JWTSigner/JWTParser (authnkit.Module).
// Hooks опциональны. Сервису с составным extractor'ом (несколько схем authn)
// Module не подходит — собирай из конструкторов, httpkit принимает один
// AuthExtractor.
var Module = fx.Module(
	"oidckit",
	fx.Provide(
		newBFF,
		NewExtractor,

		func(e *Extractor) httpkit.AuthExtractor { return e.Extract },
		fx.Annotate(NewSecurityScheme, fx.ResultTags(`group:"httpkit.security_scheme"`)),
	),
	fx.Invoke(RegisterIdentityLogAttr),
	fx.Invoke(RegisterRoutes),
)
