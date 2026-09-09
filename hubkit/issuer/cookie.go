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
		Name:     nameOr(cfg.Name, DefaultCookieName),
		Value:    value,
		Path:     "/",
		Domain:   cfg.Domain,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttlOr(cfg.TTL) / time.Second),
	}
}

// ExpiredSessionCookie — Set-Cookie на сброс сессии (logout).
func ExpiredSessionCookie(cfg CookieConfig) http.Cookie {
	c := SessionCookie("", cfg) //nolint:gosec // атрибуты наследуются из SessionCookie
	c.MaxAge = -1
	return c
}

// RefreshCookie — cookie refresh-токена: host-only (Domain пуст) + Path из
// конфига, чтобы токен уходил только на refresh/logout-роуты своего issuer'а, а
// продуктовые сабдомены его не видели. ttl — срок жизни refresh-цепочки
// (RefreshConfig.TTL).
func RefreshCookie(value string, cfg CookieConfig, ttl time.Duration) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure controlled by HUB_SESSION_COOKIE_SECURE (false only in dev)
		Name:     nameOr(cfg.RefreshName, DefaultRefreshCookieName),
		Value:    value,
		Path:     nameOr(cfg.RefreshPath, DefaultRefreshCookiePath),
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl / time.Second),
	}
}

// ExpiredRefreshCookie — Set-Cookie на сброс refresh-цепочки (logout).
func ExpiredRefreshCookie(cfg CookieConfig) http.Cookie {
	c := RefreshCookie("", cfg, 0) //nolint:gosec // Secure наследуется из cfg (см. RefreshCookie)
	c.MaxAge = -1
	return c
}

// Дефолты дублируются для конфигов, собранных литералом мимо envconfig:
// безымянная cookie на logout молча не сбросила бы сессию.
func nameOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func ttlOr(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return DefaultSessionTTL
	}
	return ttl
}
