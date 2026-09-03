package issuer_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

const (
	testIssuer   = "https://id.stit.tech"
	testAudience = "stit"
)

func genKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return priv
}

func privPEM(t *testing.T, priv *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func pubPEM(t *testing.T, priv *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func genPEM(t *testing.T) string {
	t.Helper()
	return privPEM(t, genKey(t, elliptic.P256()))
}

func keyConfig(activePEM string) issuer.KeyConfig {
	return issuer.KeyConfig{ActivePEM: activePEM, Issuer: testIssuer, Audience: testAudience}
}

// verifierFor поднимает JWKS-эндпоинт поверх signer.JWKS() и возвращает
// sso.Verifier, нацеленный на него (полная проверка контракта issuer→sso).
func verifierFor(t *testing.T, s *issuer.Signer) *sso.Verifier {
	t.Helper()
	srv := httptest.NewServer(issuer.JWKSHandler(s))
	t.Cleanup(srv.Close)
	v, err := sso.NewVerifier(sso.Config{JWKSURL: srv.URL, Issuer: testIssuer, Audience: testAudience}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func sessionClaims() map[string]any {
	return map[string]any{
		"sub":         "authentik-sub-1",
		"employee_id": "01980000-0000-7000-8000-000000000001",
		"username":    "n.sycheva",
		"name":        "Наталья Сычева",
		"email":       "n@stit.tech",
		"role":        "employee",
		"sid":         "sess-1",
	}
}

func TestSign_Verify_RoundTrip(t *testing.T) {
	s, err := issuer.NewSigner(keyConfig(genPEM(t)))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	v := verifierFor(t, s)

	tok, err := s.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.EmployeeID != "01980000-0000-7000-8000-000000000001" || id.Subject != "authentik-sub-1" ||
		id.Username != "n.sycheva" || id.Role != "employee" || id.SID != "sess-1" || id.Kind != sso.KindEmployee {
		t.Fatalf("identity mismatch: %+v", id)
	}
}

func TestSign_Expired(t *testing.T) {
	s, err := issuer.NewSigner(keyConfig(genPEM(t)))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	v := verifierFor(t, s)

	tok, err := s.Sign(sessionClaims(), -time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("протухший токен прошёл проверку")
	}
}

func TestSign_ForeignKeyRejected(t *testing.T) {
	real, err := issuer.NewSigner(keyConfig(genPEM(t)))
	if err != nil {
		t.Fatalf("NewSigner real: %v", err)
	}
	v := verifierFor(t, real)

	foreign, err := issuer.NewSigner(keyConfig(genPEM(t)))
	if err != nil {
		t.Fatalf("NewSigner foreign: %v", err)
	}
	tok, err := foreign.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("токен под чужим ключом прошёл проверку")
	}
}

// aud обязателен с обеих сторон: issuer без HUB_SESSION_AUDIENCE не стартует,
// а выпущенный токен несёт aud, который sso-verifier сверяет.
func TestSign_Audience(t *testing.T) {
	if _, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer}); !errors.Is(err, issuer.ErrNoAudience) {
		t.Fatalf("ожидали ErrNoAudience, получили %v", err)
	}

	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer, Audience: "other"})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	tok, err := s.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := verifierFor(t, s).Verify(context.Background(), tok); err == nil {
		t.Fatal("токен с чужим aud прошёл проверку")
	}
}

func TestRotation_BothKeysPublished(t *testing.T) {
	activePEM := genPEM(t)
	nextPEM := genPEM(t)

	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: activePEM, NextPEM: nextPEM, Issuer: testIssuer, Audience: testAudience})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	set := s.JWKS()
	if len(set.Keys) != 2 {
		t.Fatalf("JWKS должен содержать 2 ключа на период ротации, получили %d", len(set.Keys))
	}
	if set.Keys[0].KeyID == set.Keys[1].KeyID {
		t.Fatal("kid активного и next совпали")
	}
	v := verifierFor(t, s)

	tok, err := s.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign active: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("активный токен отвергнут: %v", err)
	}

	// После ротации next становится активным: его токены проходят против прежнего
	// JWKS (next уже был в наборе) — сессии не рвутся.
	rotated, err := issuer.NewSigner(keyConfig(nextPEM))
	if err != nil {
		t.Fatalf("NewSigner rotated: %v", err)
	}
	tok2, err := rotated.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign next: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok2); err != nil {
		t.Fatalf("токен ротационного ключа отвергнут прежним JWKS: %v", err)
	}
}

// Next-ключ публикуется по публичному PEM: приватный ключ следующего kid не
// обязан лежать на текущем инстансе.
func TestRotation_NextPublicKeyOnly(t *testing.T) {
	next := genKey(t, elliptic.P256())
	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), NextPEM: pubPEM(t, next), Issuer: testIssuer, Audience: testAudience})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	v := verifierFor(t, s)

	rotated, err := issuer.NewSigner(keyConfig(privPEM(t, next)))
	if err != nil {
		t.Fatalf("NewSigner rotated: %v", err)
	}
	if s.JWKS().Keys[1].KeyID != rotated.ActiveKID() {
		t.Fatal("kid публичного next не совпал с kid приватного ключа")
	}
	tok, err := rotated.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("токен next-ключа отвергнут: %v", err)
	}
}

func TestNewSigner_NoKey(t *testing.T) {
	if _, err := issuer.NewSigner(issuer.KeyConfig{Audience: testAudience}); !errors.Is(err, issuer.ErrNoActiveKey) {
		t.Fatalf("ожидали ErrNoActiveKey, получили %v", err)
	}
}

func TestNewSigner_BadPEM(t *testing.T) {
	if _, err := issuer.NewSigner(keyConfig("not a pem")); !errors.Is(err, issuer.ErrBadPEM) {
		t.Fatalf("ожидали ErrBadPEM, получили %v", err)
	}
}

// ES256 = P-256: ключ другой кривой отвергается на старте, а не при первой подписи.
func TestNewSigner_RejectsNonP256(t *testing.T) {
	p384 := genKey(t, elliptic.P384())
	if _, err := issuer.NewSigner(keyConfig(privPEM(t, p384))); !errors.Is(err, issuer.ErrNotEC) {
		t.Fatalf("активный P-384: ожидали ErrNotEC, получили %v", err)
	}
	cfg := keyConfig(genPEM(t))
	cfg.NextPEM = pubPEM(t, p384)
	if _, err := issuer.NewSigner(cfg); !errors.Is(err, issuer.ErrNotEC) {
		t.Fatalf("next P-384: ожидали ErrNotEC, получили %v", err)
	}
}

func TestJWKSHandler_CacheControl(t *testing.T) {
	s, err := issuer.NewSigner(keyConfig(genPEM(t)))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	rec := httptest.NewRecorder()
	issuer.JWKSHandler(s)(rec, httptest.NewRequest(http.MethodGet, issuer.JWKSPath, nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control=%q", got)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("body: %s (%v)", rec.Body.String(), err)
	}
}
