package sso

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// SecuritySchemeName — имя openapi security-scheme hub-cookie; операции
// защищаются `Security: []map[string][]string{{sso.SecuritySchemeName: {}}}`.
const SecuritySchemeName = "HubSession"

// NewSecurityScheme — запись для `components.securitySchemes` (apiKey in cookie).
func NewSecurityScheme(cfg Config) httpkit.SecuritySchemeEntry {
	cookie := cfg.Cookie
	if cookie == "" {
		cookie = "stit_session"
	}
	return httpkit.SecuritySchemeEntry{
		Name: SecuritySchemeName,
		Scheme: &huma.SecurityScheme{
			Type:        "apiKey",
			In:          "cookie",
			Name:        cookie,
			Description: "hub-session cookie (Domain=.stit.tech), выпускается id.stit.tech, верифицируется локально по JWKS.",
		},
	}
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

type verifierParams struct {
	fx.In

	Config Config
	// Doer опционален: если в DI есть httpclient.Client — используем его
	// (retry/breaker/OTel); иначе Verifier поднимает свой http.Client.
	Doer *httpclient.Client `optional:"true"`
}

func newVerifier(p verifierParams) (*Verifier, error) {
	var doer Doer
	if p.Doer != nil {
		doer = p.Doer
	}
	return NewVerifier(p.Config, doer)
}

// newCustomerVerifier — второй контур; nil в графе означает «выключен»
// (HUB_SSO_CUSTOMER_JWKS_URL пуст), NewDualExtractor это понимает.
func newCustomerVerifier(p verifierParams) (*CustomerVerifier, error) {
	var doer Doer
	if p.Doer != nil {
		doer = p.Doer
	}
	return NewCustomerVerifier(p.Config, doer)
}

// Module — drop-in верификатор hub-сессии обоих контуров: httpkit.AuthExtractor
// (cookie сотрудника или заказчика → Identity в ctx), security-scheme
// HubSession, user_id в логах.
//
// Module не provide'ит Config (asymmetry-нота как в pgkit/oidckit — caller
// добавляет `fx.Provide(sso.NewConfig)`). httpclient.Client опционален.
// Сервису с составным extractor'ом (несколько схем authn) Module не подходит —
// собирай из конструкторов: httpkit принимает один AuthExtractor.
var Module = fx.Module(
	"hubkit-sso",
	fx.Provide(
		newVerifier,
		newCustomerVerifier,
		NewDualExtractor,
		func(e *Extractor) httpkit.AuthExtractor { return e.Extract },
		fx.Annotate(NewSecurityScheme, fx.ResultTags(`group:"httpkit.security_scheme"`)),
	),
	fx.Invoke(RegisterIdentityLogAttr),
)
