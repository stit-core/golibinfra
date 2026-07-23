package sso_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

func TestExtractor_Cookie(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)
	e := sso.NewExtractor(v, sso.Config{Cookie: "stit_session"})

	tok := si.sign(t, stdClaims(), hubClaims())
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_session", Value: tok})

	ctx, err := e.Extract(r)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id, ok := sso.IdentityFrom(ctx)
	if !ok || id.Username != "n.sycheva" || id.EmployeeID == "" {
		t.Fatalf("identity=%+v ok=%v", id, ok)
	}
}

func TestExtractor_NoCookie(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	e := sso.NewExtractor(verifier(t, srv.URL), sso.Config{Cookie: "stit_session"})

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, err := e.Extract(r); !errors.Is(err, sso.ErrNoSessionCookie) {
		t.Fatalf("ожидали ErrNoSessionCookie, получили %v", err)
	}
}

func TestExtractor_BadToken(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	e := sso.NewExtractor(verifier(t, srv.URL), sso.Config{Cookie: "stit_session"})

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: "stit_session", Value: "garbage.token.value"})
	if _, err := e.Extract(r); err == nil {
		t.Fatal("мусорный токен прошёл")
	}
}

func TestConfig_LoginURLFor(t *testing.T) {
	cfg := sso.Config{LoginURL: "https://id.stit.tech/auth/login"}
	if got := cfg.LoginURLFor(""); got != "https://id.stit.tech/auth/login" {
		t.Fatalf("пустой returnTo: %s", got)
	}
	got := cfg.LoginURLFor("https://deal.stit.tech/board?x=1")
	want := "https://id.stit.tech/auth/login?rd=https%3A%2F%2Fdeal.stit.tech%2Fboard%3Fx%3D1"
	if got != want {
		t.Fatalf("LoginURLFor=%s, ожидали %s", got, want)
	}
}

func TestIdentityCtx(t *testing.T) {
	if _, ok := sso.IdentityFrom(t.Context()); ok {
		t.Fatal("в пустом ctx identity быть не должно")
	}
	in := sso.Identity{EmployeeID: "e1", Subject: "s1", Role: "admin"}
	out, ok := sso.IdentityFrom(sso.WithIdentity(t.Context(), in))
	if !ok || out != in {
		t.Fatalf("ожидали %+v, получили %+v (ok=%v)", in, out, ok)
	}
}
