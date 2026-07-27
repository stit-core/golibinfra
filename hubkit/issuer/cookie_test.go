package issuer_test

import (
	"net/http"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
)

// Два контура выпускают сессии в одном процессе-соседе: имена cookie и Path
// refresh'а обязаны приходить конфигом, а не константой.
func TestCookies_NamesFromConfig(t *testing.T) {
	emp := issuer.CookieConfig{
		Name: "stit_session", Domain: ".stit.tech", Secure: true, TTL: 15 * time.Minute,
		RefreshName: "stit_refresh", RefreshPath: "/auth",
	}
	cust := issuer.CookieConfig{
		Name: "stit_customer", Domain: ".stit.tech", Secure: true, TTL: 15 * time.Minute,
		RefreshName: "stit_customer_refresh", RefreshPath: "/auth/customer",
	}

	if c := issuer.SessionCookie("tok", cust); c.Name != "stit_customer" || c.Domain != ".stit.tech" {
		t.Fatalf("session cookie=%+v", c)
	}

	r := issuer.RefreshCookie("rtok", cust, 720*time.Hour)
	if r.Name != "stit_customer_refresh" || r.Path != "/auth/customer" {
		t.Fatalf("refresh cookie=%+v", r)
	}
	// Refresh-cookie — host-only: продуктовые сабдомены её не видят.
	if r.Domain != "" || !r.HttpOnly || !r.Secure || r.SameSite != http.SameSiteLaxMode {
		t.Fatalf("refresh cookie ослаблена: %+v", r)
	}
	if r.MaxAge != int((720 * time.Hour).Seconds()) {
		t.Fatalf("refresh MaxAge=%d", r.MaxAge)
	}

	if e := issuer.RefreshCookie("rtok", emp, time.Hour); e.Name == r.Name || e.Path == r.Path {
		t.Fatalf("два контура выпустили одинаковую refresh-cookie: %+v vs %+v", e, r)
	}

	exp := issuer.ExpiredRefreshCookie(cust)
	if exp.Name != "stit_customer_refresh" || exp.Path != "/auth/customer" || exp.MaxAge != -1 || exp.Value != "" {
		t.Fatalf("expired refresh cookie=%+v", exp)
	}
}

// Конфиг, собранный литералом (без envconfig-дефолтов), не должен выпускать
// безымянную cookie.
func TestRefreshCookie_Defaults(t *testing.T) {
	c := issuer.RefreshCookie("rtok", issuer.CookieConfig{Secure: true}, time.Hour)
	if c.Name != "stit_refresh" || c.Path != "/auth" {
		t.Fatalf("дефолты refresh-cookie не подставились: %+v", c)
	}
}
