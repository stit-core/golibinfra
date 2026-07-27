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
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

const testIssuer = "https://id.stit.tech"

// signingIdentity — тестовый ES256-ключ с производным kid + JWKS-сервер.
type signingIdentity struct {
	priv   *ecdsa.PrivateKey
	kid    string
	signer jose.Signer
	jwks   jose.JSONWebKeySet
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

func verifier(t *testing.T, jwksURL string) *sso.Verifier {
	t.Helper()
	v, err := sso.NewVerifier(sso.Config{JWKSURL: jwksURL, Issuer: testIssuer}, nil)
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

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	forged, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: other},
		(&jose.SignerOptions{}).WithHeader("kid", si.kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(forged).Claims(stdClaims()).Claims(hubClaims()).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
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
	// Токен с kid, которого нет в JWKS (битый/неизвестный kid). Verify рефрешит
	// ключи и, не найдя, возвращает ErrUnknownKey.
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v := verifier(t, srv.URL)

	bad, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: si.priv},
		(&jose.SignerOptions{}).WithHeader("kid", "no-such-kid").WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(bad).Claims(stdClaims()).Claims(hubClaims()).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, sso.ErrUnknownKey) {
		t.Fatalf("ожидали ErrUnknownKey, получили %v", err)
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

func TestVerify_Audience(t *testing.T) {
	si := newSigningIdentity(t)
	srv := si.jwksServer(t)
	v, err := sso.NewVerifier(sso.Config{JWKSURL: srv.URL, Issuer: testIssuer, Audience: "stit-hub"}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// Без aud — отвергается (audience-check включён конфигом).
	if _, err := v.Verify(context.Background(), si.sign(t, stdClaims(), hubClaims())); err == nil {
		t.Fatal("токен без ожидаемого aud прошёл проверку")
	}
	// С верным aud — проходит.
	c := stdClaims()
	c.Audience = jwt.Audience{"stit-hub"}
	if _, err := v.Verify(context.Background(), si.sign(t, c, hubClaims())); err != nil {
		t.Fatalf("токен с верным aud отвергнут: %v", err)
	}
}

func TestNewVerifier_ConfigRequired(t *testing.T) {
	if _, err := sso.NewVerifier(sso.Config{}, nil); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("ожидали ErrConfig, получили %v", err)
	}
}
