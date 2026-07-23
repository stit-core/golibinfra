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
}

// LoginURLFor строит URL входа с возвратом в продукт: <LoginURL>?rd=<returnTo>.
// Пустой returnTo → голый LoginURL (без query).
func (c Config) LoginURLFor(returnTo string) string {
	if returnTo == "" {
		return c.LoginURL
	}
	sep := "?"
	if strings.Contains(c.LoginURL, "?") {
		sep = "&"
	}
	return c.LoginURL + sep + "rd=" + url.QueryEscape(returnTo)
}
