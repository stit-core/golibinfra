package sso

import (
	"fmt"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// NewSecurityScheme — запись `components.securitySchemes` для cookie сотрудника
// (apiKey in cookie). Имя схемы — Config.SecuritySchemeName
// (`HUB_SSO_SECURITY_SCHEME`, default DefaultSecuritySchemeName): продукты
// экосистемы уже разошлись в его написании, поэтому оно настраивается.
func NewSecurityScheme(cfg Config) httpkit.SecuritySchemeEntry {
	return httpkit.SecuritySchemeEntry{
		Name: schemeNameOr(cfg.SecuritySchemeName, DefaultSecuritySchemeName),
		Scheme: &huma.SecurityScheme{
			Type:        "apiKey",
			In:          "cookie",
			Name:        cookieOr(cfg.Cookie, DefaultCookie),
			Description: "hub-session cookie of an employee (Domain=.stit.tech), issued by the employee issuer and verified locally against its JWKS.",
		},
	}
}

// NewCustomerSecurityScheme — запись `components.securitySchemes` для cookie
// заказчика; имя — Config.CustomerSecuritySchemeName.
func NewCustomerSecurityScheme(cfg Config) httpkit.SecuritySchemeEntry {
	return httpkit.SecuritySchemeEntry{
		Name: schemeNameOr(cfg.CustomerSecuritySchemeName, DefaultCustomerSecuritySchemeName),
		Scheme: &huma.SecurityScheme{
			Type:        "apiKey",
			In:          "cookie",
			Name:        cookieOr(cfg.CustomerCookie, DefaultCustomerCookie),
			Description: "hub-session cookie of a customer account, issued by the customer issuer and verified locally against its JWKS.",
		},
	}
}

// SecuritySchemes — схемы включённых контуров: сотрудник всегда, заказчик —
// только при заданном HUB_SSO_CUSTOMER_JWKS_URL (иначе OpenAPI объявлял бы
// способ аутентификации, которого у сервиса нет).
func SecuritySchemes(cfg Config) []httpkit.SecuritySchemeEntry {
	schemes := []httpkit.SecuritySchemeEntry{NewSecurityScheme(cfg)}
	if cfg.CustomerEnabled() {
		schemes = append(schemes, NewCustomerSecurityScheme(cfg))
	}
	return schemes
}

func schemeNameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

// NewConfig читает HUB_SSO_* из ENV и валидирует их (ErrConfig на старте, а не
// 401/503 на первом запросе).
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

type verifierParams struct {
	fx.In

	Config Config
	// Doer опционален: если в DI есть httpclient.Client — используем его
	// (retry/breaker/OTel); иначе Verifier поднимает свой http.Client.
	Doer *httpclient.Client `optional:"true"`
	// Options — точки расширения верификаторов (например clockkit в тестах).
	Options []Option `group:"hubkit.sso.option"`
}

func newVerifier(p verifierParams) (*Verifier, error) {
	return NewVerifier(p.Config, p.doer(), p.Options...)
}

// newCustomerVerifier — второй контур; nil в графе означает «выключен»
// (HUB_SSO_CUSTOMER_JWKS_URL пуст), NewDualExtractor это понимает.
func newCustomerVerifier(p verifierParams) (*CustomerVerifier, error) {
	return NewCustomerVerifier(p.Config, p.doer(), p.Options...)
}

func (p verifierParams) doer() Doer {
	if p.Doer != nil {
		return p.Doer
	}
	return nil
}

// OptionGroup — fx-группа опций верификаторов (`hubkit.sso.option`).
const OptionGroup = "hubkit.sso.option"

// ProvideOption добавляет Option в граф: `sso.ProvideOption(func() sso.Option {...})`.
func ProvideOption(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.ResultTags(`group:"`+OptionGroup+`"`)))
}

// Verifiers — примитивы верификации без привязки к httpkit.AuthExtractor:
// *Verifier, *CustomerVerifier (nil при выключенном контуре), *Extractor и
// security-схемы включённых контуров в группу `httpkit.security_scheme`.
//
// Это точка сборки для сервиса с составным extractor'ом: httpkit принимает
// ровно один httpkit.AuthExtractor, поэтому продукт, у которого кроме
// hub-сессии есть свой способ аутентификации, берёт Verifiers и объявляет
// AuthExtractor сам.
var Verifiers = fx.Options(
	fx.Provide(
		newVerifier,
		newCustomerVerifier,
		NewDualExtractor,
	),
	httpkit.ProvideSecurityScheme(SecuritySchemes),
)

// Module — drop-in верификатор hub-сессии обоих контуров: Verifiers +
// httpkit.AuthExtractor (cookie сотрудника или заказчика → Identity в ctx) +
// user_id в логах.
//
// Module не provide'ит Config (caller добавляет `fx.Provide(sso.NewConfig)`).
// httpclient.Client опционален.
var Module = fx.Module(
	"hubkit/sso",
	Verifiers,
	fx.Provide(func(e *Extractor) httpkit.AuthExtractor { return e.Extract }),
	fx.Invoke(RegisterIdentityLogAttr),
)
