package sso_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

const (
	testIssuer   = "https://id.stit.tech"
	testAudience = "stit"
)

// signingIdentity — тестовый ES256-ключ с производным kid + JWKS-сервер.
type signingIdentity struct {
	priv     *ecdsa.PrivateKey
	kid      string
	signer   jose.Signer
	jwks     jose.JSONWebKeySet
	jwksHits atomic.Int64
}

func newSigningIdentity(t *testing.T) *signingIdentity {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pub := jose.JSONWebKey{Key: priv.Public(), Algorithm: string(jose.ES256), Use: "sig"}
	tp, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	kid := base64.RawURLEncoding.EncodeToString(tp)
	pub.KeyID = kid

	s, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return &signingIdentity{priv: priv, kid: kid, signer: s, jwks: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{pub}}}
}

func (s *signingIdentity) jwksServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(s.jwks)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stdClaims() jwt.Claims {
	now := time.Now()
	return jwt.Claims{
		Issuer:   testIssuer,
		Subject:  "authentik-sub-1",
		Audience: jwt.Audience{testAudience},
		Expiry:   jwt.NewNumericDate(now.Add(15 * time.Minute)),
		IssuedAt: jwt.NewNumericDate(now),
	}
}

func hubClaims() map[string]any {
	return map[string]any{
		"employee_id": "01980000-0000-7000-8000-000000000001",
		"username":    "n.sycheva",
		"name":        "Наталья Сычева",
		"email":       "n@stit.tech",
		"role":        "employee",
		"sid":         "sess-1",
	}
}

func (s *signingIdentity) sign(t *testing.T, std jwt.Claims, custom map[string]any) string {
	t.Helper()
	raw, err := jwt.Signed(s.signer).Claims(std).Claims(custom).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// signWithKID подписывает тем же ключом, но с подставленным kid.
func (s *signingIdentity) signWithKID(t *testing.T, kid string, std jwt.Claims, custom map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: s.priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(std).Claims(custom).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func employeeCfg(jwksURL string) sso.Config {
	return sso.Config{JWKSURL: jwksURL, Cookie: "stit_session", Issuer: testIssuer, Audience: testAudience}
}

func verifier(t *testing.T, jwksURL string) *sso.Verifier {
	t.Helper()
	v, err := sso.NewVerifier(employeeCfg(jwksURL), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerify_Valid(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	tok := si.sign(t, stdClaims(), hubClaims())
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := sso.Identity{
		EmployeeID: "01980000-0000-7000-8000-000000000001",
		Subject:    "authentik-sub-1",
		Username:   "n.sycheva",
		Name:       "Наталья Сычева",
		Email:      "n@stit.tech",
		Role:       "employee",
		SID:        "sess-1",
		Kind:       sso.KindEmployee,
	}
	if !reflect.DeepEqual(id, want) {
		t.Fatalf("identity=%+v, ожидали %+v", id, want)
	}
}

func TestVerify_ForeignKey(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	// Токен подписан ЧУЖИМ ключом, kid — тоже чужой (нет в JWKS сервера).
	other := newSigningIdentity(t)
	tok := other.sign(t, stdClaims(), hubClaims())
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("токен под чужой подписью прошёл проверку")
	}
}

func TestVerify_ForeignKeySameKID(t *testing.T) {
	// Атака: чужой ключ, но kid подставлен под наш (совпадает с JWKS). Подпись
	// не сойдётся с публичным ключом из JWKS — verify обязан отвергнуть.
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	other := newSigningIdentity(t)
	raw := other.signWithKID(t, si.kid, stdClaims(), hubClaims())
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("подделка с подставленным kid прошла проверку")
	}
}

func TestVerify_Expired(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	c := stdClaims()
	c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	if _, err := v.Verify(context.Background(), si.sign(t, c, hubClaims())); err == nil {
		t.Fatal("протухший токен прошёл проверку")
	}
}

func TestVerify_UnknownKID(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	raw := si.signWithKID(t, "no-such-kid", stdClaims(), hubClaims())
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, sso.ErrUnknownKey) {
		t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
	}
}

// Шторм токенов с неизвестным kid — одна загрузка JWKS на интервал, а не
// запрос к хабу на каждый токен.
func TestVerify_UnknownKIDStorm_SingleJWKSFetch(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			raw := si.signWithKID(t, "attacker-kid", stdClaims(), hubClaims())
			if _, err := v.Verify(context.Background(), raw); !errors.Is(err, sso.ErrUnknownKey) {
				t.Errorf("ожидали ErrUnknownKey, получили %v", err)
			}
		})
	}
	wg.Wait()
	if got := si.jwksHits.Load(); got != 1 {
		t.Fatalf("100 неизвестных kid → %d загрузок JWKS, ожидали 1", got)
	}
	if _, err := v.Verify(context.Background(), si.sign(t, stdClaims(), hubClaims())); err != nil {
		t.Fatalf("валидный токен после шторма: %v", err)
	}
	if got := si.jwksHits.Load(); got != 1 {
		t.Fatalf("валидный токен вызвал загрузку: %d", got)
	}
}

func TestVerify_WrongIssuer(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	c := stdClaims()
	c.Issuer = "https://evil.example"
	if _, err := v.Verify(context.Background(), si.sign(t, c, hubClaims())); err == nil {
		t.Fatal("токен с чужим iss прошёл проверку")
	}
}

// aud проверяется всегда: токен без aud или с чужим aud отвергается.
func TestVerify_Audience(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	c := stdClaims()
	c.Audience = nil
	if _, err := v.Verify(context.Background(), si.sign(t, c, hubClaims())); err == nil {
		t.Fatal("токен без aud прошёл проверку")
	}
	c.Audience = jwt.Audience{"other-product"}
	if _, err := v.Verify(context.Background(), si.sign(t, c, hubClaims())); err == nil {
		t.Fatal("токен с чужим aud прошёл проверку")
	}
}

func TestNewVerifier_ConfigRequired(t *testing.T) {
	if _, err := sso.NewVerifier(sso.Config{Audience: testAudience}, nil); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("ожидали ErrConfig, получили %v", err)
	}
	if _, err := sso.NewVerifier(sso.Config{JWKSURL: "https://id.stit.tech/jwks"}, nil); !errors.Is(err, sso.ErrAudience) {
		t.Fatalf("ожидали ErrAudience, получили %v", err)
	}
}
