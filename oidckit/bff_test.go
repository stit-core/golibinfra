package oidckit

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

const sessionSecret = "session-secret-of-at-least-32-bytes!!"

func memStore() StateStore { return NewCacheStateStore(cachekit.NewMemoryCache()) }

func sessionSigner(t *testing.T) *authnkit.HS256JWT {
	t.Helper()
	s, err := authnkit.NewHS256JWT(authnkit.JWTConfig{Secret: sessionSecret})
	if err != nil {
		t.Fatalf("NewHS256JWT: %v", err)
	}
	return s
}

type stubIDP struct {
	url    string
	signer jose.Signer
	nonce  string
}

func newStubIDP(t *testing.T) *stubIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}

	idp := &stubIDP{signer: signer}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	idp.url = srv.URL

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"jwks_uri":               srv.URL + "/jwks",
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		claims := jwt.Claims{
			Issuer:   srv.URL,
			Subject:  "emp-99",
			Audience: jwt.Audience{"svc"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		}
		raw, _ := jwt.Signed(idp.signer).Claims(claims).
			Claims(map[string]any{"preferred_username": "b.bobov", "name": "Bob", "email": "bob@x.io", "nonce": idp.nonce}).Serialize()
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": raw})
	})
	return idp
}

func newTestBFF(t *testing.T, idp *stubIDP, store StateStore, hooks Hooks) *BFF {
	t.Helper()
	signer := sessionSigner(t)
	cfg := Config{
		Issuer:       idp.url,
		ClientID:     "svc",
		ClientSecret: "cs",
		RedirectURL:  "http://app/auth/callback",
		PostLoginURL: "/app",
		SessionTTL:   12 * time.Hour,
	}
	return NewBFF(cfg, signer, store, CookieConfig{Secure: false}, hooks, logkit.Nop())
}

func parseSession(t *testing.T, token string) map[string]any {
	t.Helper()
	claims, err := sessionSigner(t).Parse(token)
	if err != nil {
		t.Fatalf("parse session: %v", err)
	}
	return claims
}

func TestBFF_LoginCallback(t *testing.T) {
	idp := newStubIDP(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})
	ctx := context.Background()

	// login → redirect на idp, state+nonce сохранены.
	loginOut, err := b.login(ctx, &loginInput{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	u, err := url.Parse(loginOut.Location)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	state, nonce := u.Query().Get("state"), u.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("state=%q nonce=%q — пустые", state, nonce)
	}
	if got := u.Query().Get("response_type"); got != "code" {
		t.Fatalf("response_type=%q", got)
	}

	// idp вернёт id_token с этим nonce (как реальный IdP, эхом из auth-запроса).
	idp.nonce = nonce

	// callback → обмен кода, session-cookie выставлена.
	cbOut, err := b.callback(ctx, &callbackInput{Code: "auth-code", State: state})
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if cbOut.Location != "/app" {
		t.Fatalf("location=%q", cbOut.Location)
	}
	if len(cbOut.SetCookie) != 1 {
		t.Fatalf("cookies=%d", len(cbOut.SetCookie))
	}
	session := cbOut.SetCookie[0]
	if session.Name != CookieAccess || !session.HttpOnly {
		t.Fatalf("cookie: %+v", session)
	}

	// session-cookie парсится тем же секретом (как Extractor), sub = OIDC subject;
	// имя/логин/email из id_token'а доезжают в session-JWT.
	claims := parseSession(t, session.Value)
	for k, want := range map[string]string{"sub": "emp-99", "preferred_username": "b.bobov", "name": "Bob", "email": "bob@x.io"} {
		if claims[k] != want {
			t.Fatalf("claim %s=%v, ожидали %q", k, claims[k], want)
		}
	}

	// state одноразовый — второй callback отвергается.
	if _, err := b.callback(ctx, &callbackInput{Code: "auth-code", State: state}); err == nil {
		t.Fatal("повторный callback с тем же state прошёл")
	}
}

// Hooks.SessionClaims дополняет session-JWT продуктовыми claims (например role).
func TestBFF_SessionClaimsHook(t *testing.T) {
	idp := newStubIDP(t)
	hooks := Hooks{SessionClaims: func(c Claims) map[string]any {
		if c.Username == "b.bobov" {
			return map[string]any{"role": "admin"}
		}
		return map[string]any{"role": "client"}
	}}
	b := newTestBFF(t, idp, memStore(), hooks)
	ctx := context.Background()

	loginOut, err := b.login(ctx, &loginInput{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	u, _ := url.Parse(loginOut.Location)
	idp.nonce = u.Query().Get("nonce")

	cbOut, err := b.callback(ctx, &callbackInput{Code: "auth-code", State: u.Query().Get("state")})
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if claims := parseSession(t, cbOut.SetCookie[0].Value); claims["role"] != "admin" {
		t.Fatalf("role=%v, ожидали admin", claims["role"])
	}
}

// Hooks.OnLogin выполняется до выдачи cookie; ошибка отменяет вход.
func TestBFF_OnLoginHook(t *testing.T) {
	run := func(t *testing.T, hookErr error) (called bool, cbErr error) {
		idp := newStubIDP(t)
		hooks := Hooks{OnLogin: func(_ context.Context, c Claims) error {
			called = true
			if c.Subject != "emp-99" {
				t.Fatalf("hook claims: %+v", c)
			}
			return hookErr
		}}
		b := newTestBFF(t, idp, memStore(), hooks)
		ctx := context.Background()

		loginOut, err := b.login(ctx, &loginInput{})
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		u, _ := url.Parse(loginOut.Location)
		idp.nonce = u.Query().Get("nonce")
		_, cbErr = b.callback(ctx, &callbackInput{Code: "auth-code", State: u.Query().Get("state")})
		return called, cbErr
	}

	t.Run("успех — hook вызван, вход прошёл", func(t *testing.T) {
		called, err := run(t, nil)
		if !called || err != nil {
			t.Fatalf("called=%v err=%v", called, err)
		}
	})
	t.Run("ошибка hook'а отменяет вход", func(t *testing.T) {
		called, err := run(t, context.DeadlineExceeded)
		if !called || err == nil {
			t.Fatalf("called=%v err=%v", called, err)
		}
	})
}

// return_to из login доезжает до redirect'а callback'а; наружные URL отбрасываются.
func TestBFF_ReturnTo(t *testing.T) {
	cases := []struct {
		name, returnTo, want string
	}{
		{"внутренний путь", "/join/abc", "/join/abc"},
		{"внешний URL отброшен", "https://evil.example/x", "/app"},
		{"protocol-relative отброшен", "//evil.example/x", "/app"},
		{"пусто — дефолт", "", "/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newStubIDP(t)
			b := newTestBFF(t, idp, memStore(), Hooks{})
			ctx := context.Background()

			loginOut, err := b.login(ctx, &loginInput{ReturnTo: tc.returnTo})
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			u, _ := url.Parse(loginOut.Location)
			idp.nonce = u.Query().Get("nonce")

			cbOut, err := b.callback(ctx, &callbackInput{Code: "auth-code", State: u.Query().Get("state")})
			if err != nil {
				t.Fatalf("callback: %v", err)
			}
			if cbOut.Location != tc.want {
				t.Fatalf("location=%q, ожидали %q", cbOut.Location, tc.want)
			}
		})
	}
}

func TestBFF_UnknownState(t *testing.T) {
	b := newTestBFF(t, newStubIDP(t), memStore(), Hooks{})
	if _, err := b.callback(context.Background(), &callbackInput{Code: "c", State: "nope"}); err == nil {
		t.Fatal("callback с неизвестным state прошёл")
	}
}

func TestBFF_Disabled(t *testing.T) {
	b := NewBFF(Config{}, sessionSigner(t), memStore(), CookieConfig{}, Hooks{}, logkit.Nop())
	if _, err := b.login(context.Background(), &loginInput{}); err == nil {
		t.Fatal("login при выключенном OIDC прошёл")
	}
}

func TestBFF_Me(t *testing.T) {
	b := newTestBFF(t, newStubIDP(t), memStore(), Hooks{})
	ctx := WithIdentity(context.Background(), Identity{Subject: "emp-1", Username: "n.sycheva", Name: "Наталья Сычева", Role: "admin"})

	out, err := b.me(ctx, &struct{}{})
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	d := out.Body.Data
	if d.Subject != "emp-1" || d.Username != "n.sycheva" || d.Name != "Наталья Сычева" || d.Role != "admin" {
		t.Fatalf("me: %+v", d)
	}

	// без session-cookie → 401.
	if _, err := b.me(context.Background(), &struct{}{}); err == nil {
		t.Fatal("me без identity прошёл")
	}
}

func TestBFF_Logout(t *testing.T) {
	b := newTestBFF(t, newStubIDP(t), memStore(), Hooks{})
	out, err := b.logout(context.Background(), &struct{}{})
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(out.SetCookie) == 0 {
		t.Fatal("logout не сбросил cookie")
	}
	for _, c := range out.SetCookie {
		if c.MaxAge != -1 {
			t.Fatalf("cookie %s: MaxAge=%d, ожидали -1", c.Name, c.MaxAge)
		}
	}
}

func TestConfig_RoutePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/auth"},
		{"/auth", "/auth"},
		{"/sso/", "/sso"},
	}
	for _, tc := range cases {
		if got := (Config{RoutePrefix: tc.in}).routePrefix(); got != tc.want {
			t.Fatalf("routePrefix(%q)=%q, ожидали %q", tc.in, got, tc.want)
		}
	}
}
