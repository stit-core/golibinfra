package sso

import (
	"net/url"
	"strings"
)

// Config — env-конфиг верификатора hub-сессии (имена HUB_SSO_* — public API).
type Config struct {
	// JWKSURL — JWKS employee-backend; обязателен.
	JWKSURL string `envconfig:"HUB_SSO_JWKS_URL"`
	// Cookie — имя cookie с hub-session JWT.
	Cookie string `default:"stit_session" envconfig:"HUB_SSO_COOKIE"`
	// Issuer — ожидаемый iss (проверяется, если непусто).
	Issuer string `default:"https://id.stit.tech" envconfig:"HUB_SSO_ISSUER"`
	// Audience — ожидаемый aud (проверяется, только если непусто).
	Audience string `envconfig:"HUB_SSO_AUDIENCE"`
	// LoginURL — точка входа SSO для login-redirect хелпера.
	LoginURL string `default:"https://id.stit.tech/auth/login" envconfig:"HUB_SSO_LOGIN_URL"`

	// CustomerJWKSURL — JWKS company-backend (issuer сессии заказчика). Пусто →
	// контур заказчика выключен: продукт ведёт себя ровно как без него (продукты
	// входят в контур не одновременно).
	CustomerJWKSURL string `envconfig:"HUB_SSO_CUSTOMER_JWKS_URL"`
	// CustomerCookie — имя cookie сессии заказчика (свой слот: сотрудник и
	// заказчик могут быть в одном браузере, один вход не выбивает другой).
	CustomerCookie string `default:"stit_customer" envconfig:"HUB_SSO_CUSTOMER_COOKIE"`
	// CustomerIssuer — ожидаемый iss сессии заказчика; для включённого контура
	// обязателен (токен чужого контура не должен приниматься никогда).
	CustomerIssuer string `default:"https://api.hub.stit.tech" envconfig:"HUB_SSO_CUSTOMER_ISSUER"`
	// CustomerLoginURL — экран входа заказчика для login-redirect хелпера.
	CustomerLoginURL string `default:"https://hub.stit.tech/customer/login" envconfig:"HUB_SSO_CUSTOMER_LOGIN_URL"`
}

// CustomerEnabled — включён ли контур заказчика (задан его JWKS).
func (c Config) CustomerEnabled() bool { return c.CustomerJWKSURL != "" }

// LoginURLFor строит URL входа сотрудника с возвратом в продукт:
// <LoginURL>?rd=<returnTo>. Пустой returnTo → голый LoginURL (без query).
func (c Config) LoginURLFor(returnTo string) string { return withReturnTo(c.LoginURL, returnTo) }

// CustomerLoginURLFor — то же для экрана входа заказчика.
func (c Config) CustomerLoginURLFor(returnTo string) string {
	return withReturnTo(c.CustomerLoginURL, returnTo)
}

func withReturnTo(base, returnTo string) string {
	if returnTo == "" {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "rd=" + url.QueryEscape(returnTo)
}
