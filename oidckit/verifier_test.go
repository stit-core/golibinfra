package oidckit_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/oidckit"
)

const (
	kid      = "test-key-1"
	clientID = "svc"
)

type idp struct {
	server       *httptest.Server
	key          *rsa.PrivateKey
	signer       jose.Signer
	issuedNonce  string // nonce, который IdP кладёт в id_token на /token
	issuedIssuer string // iss в id_token (для override в негативных тестах)
	jwksHits     atomic.Int64
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig",
	}}}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	i := &idp{server: srv, key: key, signer: signer, issuedIssuer: srv.URL}

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"jwks_uri":               srv.URL + "/jwks",
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		i.jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		custom := map[string]any{"name": "Alice", "email": "alice@x.io"}
		if i.issuedNonce != "" {
			custom["nonce"] = i.issuedNonce
		}
		raw := i.token(t, i.claims(), custom)
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": raw, "token_type": "Bearer"})
	})
	return i
}

func (i *idp) claims() jwt.Claims {
	return jwt.Claims{
		Issuer:   i.issuedIssuer,
		Subject:  "employee-sub-1",
		Audience: jwt.Audience{clientID},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}
}

func (i *idp) token(t *testing.T, claims jwt.Claims, custom map[string]any) string {
	t.Helper()
	raw, err := jwt.Signed(i.signer).Claims(claims).Claims(custom).Serialize()
	if err != nil {
		t.Fatalf("sign id token: %v", err)
	}
	return raw
}

// tokenWithKID подписывает тем же ключом, но с другим kid в заголовке.
func (i *idp) tokenWithKID(t *testing.T, kid string) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(i.claims()).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func (i *idp) verifier(t *testing.T) *oidckit.Verifier {
	t.Helper()
	v, err := oidckit.NewVerifier(context.Background(), oidckit.RPConfig{
		Issuer:       i.server.URL,
		ClientID:     clientID,
		ClientSecret: "secret",
		RedirectURL:  "http://app/callback",
	}, i.server.Client())
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_Valid(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	tok := i.token(t, i.claims(), map[string]any{"preferred_username": "a.ivanova", "name": "Alice", "email": "alice@x.io"})

	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Subject != "employee-sub-1" || c.Username != "a.ivanova" || c.Name != "Alice" || c.Email != "alice@x.io" {
		t.Fatalf("claims: %+v", c)
	}
}

func TestVerify_WrongAudience(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	c := i.claims()
	c.Audience = jwt.Audience{"other-app"}
	if _, err := v.Verify(context.Background(), i.token(t, c, nil)); err == nil {
		t.Fatal("токен с чужим aud прошёл проверку")
	}
}

func TestVerify_Expired(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	c := i.claims()
	c.Expiry = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-3 * time.Hour))
	if _, err := v.Verify(context.Background(), i.token(t, c, nil)); err == nil {
		t.Fatal("протухший токен прошёл проверку")
	}
}

func TestVerify_ForeignSignature(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	other := newIDP(t)
	if _, err := v.Verify(context.Background(), other.token(t, i.claims(), nil)); err == nil {
		t.Fatal("токен под чужой подписью прошёл проверку")
	}
}

// Шторм токенов с неизвестным kid не превращается в шторм запросов к JWKS:
// одна загрузка на интервал, остальные промахи отвечают из кэша.
func TestVerify_UnknownKIDStorm_SingleJWKSFetch(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	initial := i.jwksHits.Load()

	var wg sync.WaitGroup
	for n := range 100 {
		wg.Go(func() {
			tok := i.tokenWithKID(t, "unknown-"+strings.Repeat("x", n%3))
			if _, err := v.Verify(context.Background(), tok); !errors.Is(err, oidckit.ErrUnknownKey) {
				t.Errorf("ожидали ErrUnknownKey, получили %v", err)
			}
		})
	}
	wg.Wait()
	if got := i.jwksHits.Load() - initial; got > 1 {
		t.Fatalf("100 неизвестных kid → %d загрузок JWKS, ожидали не больше 1", got)
	}
	// Известный ключ по-прежнему из кэша.
	if _, err := v.Verify(context.Background(), i.token(t, i.claims(), nil)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestNewVerifier_Config(t *testing.T) {
	if _, err := oidckit.NewVerifier(context.Background(), oidckit.RPConfig{ClientID: "x"}, nil); !errors.Is(err, oidckit.ErrRPConfig) {
		t.Fatalf("пустой Issuer: ожидали ErrRPConfig, получили %v", err)
	}
}

func TestNewVerifier_NilClient(t *testing.T) {
	i := newIDP(t)
	v, err := oidckit.NewVerifier(context.Background(), oidckit.RPConfig{Issuer: i.server.URL, ClientID: clientID}, nil)
	if err != nil {
		t.Fatalf("NewVerifier с nil client: %v", err)
	}
	if _, err := v.Verify(context.Background(), i.token(t, i.claims(), nil)); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAuthCodeURL(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(t)
	got := v.AuthCodeURL("state-1", "nonce-1", "challenge-1")

	if !strings.HasPrefix(got, i.server.URL+"/authorize?") {
		t.Fatalf("prefix: %s", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": clientID, "redirect_uri": "http://app/callback", "response_type": "code",
		"state": "state-1", "nonce": "nonce-1", "code_challenge": "challenge-1", "code_challenge_method": "S256",
	} {
		if q.Get(k) != want {
			t.Fatalf("%s=%q, ожидали %q", k, q.Get(k), want)
		}
	}
}

func TestExchange(t *testing.T) {
	i := newIDP(t)
	i.issuedNonce = "nonce-abc"
	v := i.verifier(t)

	c, err := v.Exchange(context.Background(), "auth-code", "verifier", "nonce-abc")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if c.Subject != "employee-sub-1" || c.Name != "Alice" {
		t.Fatalf("claims: %+v", c)
	}
}

func TestExchange_NonceMismatch(t *testing.T) {
	i := newIDP(t)
	i.issuedNonce = "nonce-real"
	v := i.verifier(t)

	if _, err := v.Exchange(context.Background(), "auth-code", "verifier", "nonce-expected"); !errors.Is(err, oidckit.ErrNonceMismatch) {
		t.Fatalf("nonce mismatch: ожидали ErrNonceMismatch, получили %v", err)
	}
}

func TestPKCE(t *testing.T) {
	verifier, challenge, err := oidckit.PKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("challenge != S256(verifier)")
	}
	other, _, _ := oidckit.PKCE()
	if verifier == other {
		t.Fatal("PKCE verifier не случаен")
	}
}
