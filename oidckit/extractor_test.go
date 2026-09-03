package oidckit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExtractor(t *testing.T) {
	jwt := sessionSigner(t)
	e := NewExtractor(jwt)

	token, err := jwt.Sign(map[string]any{
		"sub":                "emp-1",
		"preferred_username": "n.sycheva",
		"name":               "Наталья Сычева",
		"email":              "n@x.io",
		"role":               "admin",
	}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieAccess, Value: token})

	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id, ok := IdentityFrom(ctx)
	if !ok {
		t.Fatal("identity не в ctx")
	}
	want := Identity{Subject: "emp-1", Username: "n.sycheva", Name: "Наталья Сычева", Email: "n@x.io", Role: "admin"}
	if id != want {
		t.Fatalf("identity=%+v, ожидали %+v", id, want)
	}
}

func TestExtractor_NoCookie(t *testing.T) {
	e := NewExtractor(sessionSigner(t))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, err := e.Extract(r); !errors.Is(err, ErrNoSessionCookie) {
		t.Fatalf("ожидали ErrNoSessionCookie, получили %v", err)
	}
}

func TestExtractor_BadToken(t *testing.T) {
	e := NewExtractor(sessionSigner(t))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: CookieAccess, Value: "garbage"})
	if _, err := e.Extract(r); err == nil {
		t.Fatal("мусорный токен прошёл")
	}
}

func TestIdentityCtx(t *testing.T) {
	if _, ok := IdentityFrom(t.Context()); ok {
		t.Fatal("в пустом ctx identity быть не должно")
	}
	in := Identity{Subject: "u1", Role: "admin"}
	out, ok := IdentityFrom(WithIdentity(t.Context(), in))
	if !ok || out != in {
		t.Fatalf("ожидали %+v, получили %+v (ok=%v)", in, out, ok)
	}
}

func TestSessionCookie_SecureFromConfig(t *testing.T) {
	c := SessionCookie("tok", CookieConfig{Secure: true})
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Name != CookieAccess {
		t.Fatalf("cookie ослаблена: %+v", c)
	}
	for _, exp := range ExpiredSessionCookies(CookieConfig{Secure: true}) {
		if !exp.Secure || exp.MaxAge != -1 {
			t.Fatalf("expired cookie: %+v", exp)
		}
	}
}
