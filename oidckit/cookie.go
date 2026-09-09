package oidckit

import (
	"net/http"
	"time"
)

// SessionCookie — httpOnly-cookie с session-JWT: Path=/, Max-Age по ttl,
// SameSite и Secure по CookieConfig.
func SessionCookie(value string, cfg CookieConfig, ttl time.Duration) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure снимается только COOKIE_INSECURE (dev по http)
		Name:     cfg.SessionCookieName(),
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: cfg.sameSite(),
		Secure:   cfg.Secure(),
		MaxAge:   maxAgeSeconds(ttl),
	}
}

// StateCookie — flow-cookie логин-флоу: привязывает callback к браузеру,
// который начинал вход. SameSite=Lax обязателен — callback приходит от idp
// cross-site top-level переходом, при Strict cookie не отправится.
func StateCookie(state string, cfg CookieConfig, ttl time.Duration) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure снимается только COOKIE_INSECURE (dev по http)
		Name:     cfg.StateCookieName(),
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cfg.Secure(),
		MaxAge:   maxAgeSeconds(ttl),
	}
}

// ExpiredSessionCookies — Set-Cookie на сброс сессии (logout): session-cookie и
// flow-cookie логин-флоу.
func ExpiredSessionCookies(cfg CookieConfig) []http.Cookie {
	return []http.Cookie{
		expiredCookie(cfg.SessionCookieName(), cfg, cfg.sameSite()),
		expiredCookie(cfg.StateCookieName(), cfg, http.SameSiteLaxMode),
	}
}

func expiredCookie(name string, cfg CookieConfig, sameSite http.SameSite) http.Cookie {
	return http.Cookie{ //nolint:gosec // Secure снимается только COOKIE_INSECURE (dev по http)
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: sameSite,
		Secure:   cfg.Secure(),
		MaxAge:   -1,
	}
}

func maxAgeSeconds(ttl time.Duration) int {
	if secs := int(ttl.Seconds()); secs > 0 {
		return secs
	}
	return 1
}
