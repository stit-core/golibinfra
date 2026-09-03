package oidckit

import "net/http"

const (
	// CookieAccess — httpOnly-cookie c session-JWT (единственная сессионная кука BFF).
	CookieAccess = "access_token"
	// CookieRefresh чистится на logout заодно с access: у сервисов с отдельным
	// refresh-контуром не должно оставаться живой пары после выхода.
	CookieRefresh = "refresh_token"
)

// CookieConfig: Secure выключается только для локальной разработки по http
// (COOKIE_SECURE=false); литерал CookieConfig{} даёт Secure=false — заполняй явно.
type CookieConfig struct {
	Secure bool `default:"true" envconfig:"COOKIE_SECURE"`
}

// SessionCookie — httpOnly session-cookie (SameSite=Strict; Secure по COOKIE_SECURE).
func SessionCookie(value string, cfg CookieConfig) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure по COOKIE_SECURE: default true, false только в dev
		Name:     CookieAccess,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   cfg.Secure,
	}
}

// ExpiredSessionCookies — Set-Cookie на сброс сессии (logout): access + refresh.
func ExpiredSessionCookies(cfg CookieConfig) []http.Cookie {
	return []http.Cookie{expiredCookie(CookieAccess, cfg), expiredCookie(CookieRefresh, cfg)}
}

func expiredCookie(name string, cfg CookieConfig) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure по COOKIE_SECURE: default true, false только в dev
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   cfg.Secure,
		MaxAge:   -1,
	}
}
