package issuer_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

const testIssuer = "https://id.stit.tech"

func genPEM(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// jwksServerFor поднимает JWKS-эндпоинт поверх signer.JWKS() и возвращает
// sso.Verifier, нацеленный на него (полная проверка контракта issuer→sso).
func verifierFor(t *testing.T, s *issuer.Signer) *sso.Verifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(s.JWKS())
	}))
	t.Cleanup(srv.Close)
	v, err := sso.NewVerifier(sso.Config{JWKSURL: srv.URL, Issuer: testIssuer}, nil)
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
	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer})
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
		id.Username != "n.sycheva" || id.Role != "employee" || id.SID != "sess-1" {
		t.Fatalf("identity mismatch: %+v", id)
	}
}

func TestSign_Expired(t *testing.T) {
	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer})
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
	// Верификатор знает JWKS одного хаба; токен подписан другим — отказ.
	real, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer})
	if err != nil {
		t.Fatalf("NewSigner real: %v", err)
	}
	v := verifierFor(t, real)

	foreign, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer})
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

func TestRotation_BothKeysPublished(t *testing.T) {
	activePEM := genPEM(t)
	nextPEM := genPEM(t)

	// Текущее состояние: active = activePEM, next = nextPEM (оба в JWKS).
	s, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: activePEM, NextPEM: nextPEM, Issuer: testIssuer})
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

	// Токен, подписанный текущим активным, верифицируется.
	tok, err := s.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign active: %v", err)
	}
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("активный токен отвергнут: %v", err)
	}

	// После ротации next становится активным. Токен, подписанный им, всё ещё
	// верифицируется против прежнего JWKS (next был в наборе) — сессии не рвутся.
	rotated, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: nextPEM, Issuer: testIssuer})
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

func TestNewSigner_NoKey(t *testing.T) {
	if _, err := issuer.NewSigner(issuer.KeyConfig{}); err == nil {
		t.Fatal("ожидали ошибку при пустом активном ключе")
	}
}

func TestNewSigner_BadPEM(t *testing.T) {
	if _, err := issuer.NewSigner(issuer.KeyConfig{ActivePEM: "not a pem"}); err == nil {
		t.Fatal("ожидали ошибку на битом PEM")
	}
}
