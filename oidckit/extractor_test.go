package oidckit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/authnkit"
)

func sessionToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	token, err := sessionSigner(t).Sign(t.Context(), claims, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

func requestWithCookie(name, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r
}

func TestExtractor(t *testing.T) {
	e := NewExtractor(sessionSigner(t), devCookies)
	token := sessionToken(t, map[string]any{
		"sub":                   "u-1",
		"preferred_username":    "j.doe",
		"name":                  "Jane Doe",
		"email":                 "j.doe@example.test",
		"role":                  "admin",
		authnkit.ClaimTokenType: SessionTokenType,
	})

	ctx, err := e.Extract(requestWithCookie(CookieAccess, token))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	id, ok := IdentityFrom(ctx)
	if !ok {
		t.Fatal("identity не в ctx")
	}
	want := Identity{Subject: "u-1", Username: "j.doe", Name: "Jane Doe", Email: "j.doe@example.test", Role: "admin"}
	if id != want {
		t.Fatalf("identity=%+v, ожидали %+v", id, want)
	}
}

// Token confusion: под тем же JWT_SECRET живут токены других схем — сессией
// считается только токен с typ=oidc_session и непустым sub.
func TestExtractor_RejectsForeignTokens(t *testing.T) {
	signer := sessionSigner(t)
	e := NewExtractor(signer, devCookies)

	access, err := signer.SignAccess(t.Context(), map[string]any{"sub": "u-1"})
	if err != nil {
		t.Fatalf("SignAccess: %v", err)
	}

	cases := map[string]string{
		"access-токен парольной схемы": access,
		"токен чужого typ":             sessionToken(t, map[string]any{"sub": "u-1", authnkit.ClaimTokenType: "invite"}),
		"токен без typ":                sessionToken(t, map[string]any{"sub": "u-1"}),
		"сессия без sub":               sessionToken(t, map[string]any{authnkit.ClaimTokenType: SessionTokenType}),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := e.Extract(requestWithCookie(CookieAccess, token)); !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("ожидали ErrInvalidSession, получили %v", err)
			}
		})
	}
}

func TestExtractor_NoCookie(t *testing.T) {
	e := NewExtractor(sessionSigner(t), devCookies)

	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, err := e.Extract(r); !errors.Is(err, ErrNoSessionCookie) {
		t.Fatalf("ожидали ErrNoSessionCookie, получили %v", err)
	}
	// Cookie под именем чужой политики (без __Host-) не считается сессией.
	secure := NewExtractor(sessionSigner(t), CookieConfig{})
	if _, err := secure.Extract(requestWithCookie(CookieAccess, "x")); !errors.Is(err, ErrNoSessionCookie) {
		t.Fatalf("ожидали ErrNoSessionCookie, получили %v", err)
	}
}

func TestExtractor_BadToken(t *testing.T) {
	e := NewExtractor(sessionSigner(t), devCookies)
	if _, err := e.Extract(requestWithCookie(CookieAccess, "garbage")); err == nil {
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

func TestMustIdentityFrom_PanicsWithoutIdentity(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustIdentityFrom без identity не паникует")
		}
	}()
	MustIdentityFrom(t.Context())
}
