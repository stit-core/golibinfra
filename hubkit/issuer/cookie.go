package issuer

import (
	"net/http"
	"time"
)

// SessionCookie — hub-session cookie на весь контур *.stit.tech:
// Domain=.stit.tech, HttpOnly, Secure (по конфигу), SameSite=Lax
// (Lax, не Strict: переход по внешней ссылке на продукт должен нести сессию).
func SessionCookie(value string, cfg CookieConfig) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure controlled by HUB_SESSION_COOKIE_SECURE (false only in dev)
		Name:     cfg.Name,
		Value:    value,
		Path:     "/",
		Domain:   cfg.Domain,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cfg.TTL / time.Second),
	}
}

// ExpiredSessionCookie — Set-Cookie на сброс сессии (logout).
func ExpiredSessionCookie(cfg CookieConfig) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure controlled by HUB_SESSION_COOKIE_SECURE (false only in dev)
		Name:     cfg.Name,
		Value:    "",
		Path:     "/",
		Domain:   cfg.Domain,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}
