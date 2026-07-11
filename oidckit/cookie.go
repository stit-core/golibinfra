package oidckit

import "net/http"

const (
	// CookieAccess — httpOnly-cookie c session-JWT (единственная сессионная кука BFF).
	CookieAccess = "access_token"
	// CookieRefresh чистится на logout заодно с access: у сервисов с отдельным
	// refresh-контуром не должно оставаться живой пары после выхода.
	CookieRefresh = "refresh_token"
)

type CookieConfig struct {
	Secure bool `default:"false" envconfig:"COOKIE_SECURE"`
}

// SessionCookie — httpOnly session-cookie (SameSite=Strict; Secure по COOKIE_SECURE).
func SessionCookie(value string, cfg CookieConfig) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure controlled by COOKIE_SECURE env (false in dev)
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
	return http.Cookie{ //nolint:gosec // Secure controlled by COOKIE_SECURE env (false in dev)
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   cfg.Secure,
		MaxAge:   -1,
	}
}
