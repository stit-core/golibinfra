package issuer_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
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

func sec1PEM(t *testing.T, priv *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal sec1: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
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
	return issuer.KeyConfig{
		ActivePEM:  activePEM,
		Issuer:     testIssuer,
		Audience:   testAudience,
		JWKSMaxAge: issuer.DefaultJWKSMaxAge,
	}
}

func newSigner(t *testing.T, cfg issuer.KeyConfig, opts ...issuer.Option) *issuer.Signer {
	t.Helper()
	s, err := issuer.NewSigner(cfg, opts...)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// verifierFor поднимает JWKS-эндпоинт поверх signer.JWKS() и возвращает
// sso.Verifier, нацеленный на него (полная проверка контракта issuer→sso).
func verifierFor(t *testing.T, s *issuer.Signer, opts ...sso.Option) *sso.Verifier {
	t.Helper()
	srv := httptest.NewServer(issuer.JWKSHandler(s))
	t.Cleanup(srv.Close)
	cfg := sso.Config{
		JWKSURL:            srv.URL,
		Cookie:             sso.DefaultCookie,
		Issuer:             testIssuer,
		Audience:           testAudience,
		LoginURL:           testIssuer + "/auth/login",
		Leeway:             sso.DefaultLeeway,
		KeyGrace:           sso.DefaultKeyGrace,
		SecuritySchemeName: sso.DefaultSecuritySchemeName,
	}
	v, err := sso.NewVerifier(cfg, srv.Client(), opts...)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func sessionClaims() map[string]any {
	return issuer.Session{
		SID:        "sess-1",
		Subject:    "authentik-sub-1",
		EmployeeID: "01980000-0000-7000-8000-000000000001",
		Username:   "n.sycheva",
		Name:       "Наталья Сычева",
		Email:      "n@stit.tech",
		Role:       "employee",
	}.Claims()
}

func TestSign_Verify_RoundTrip(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	raw, err := s.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	id, err := verifierFor(t, s).Verify(t.Context(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "authentik-sub-1" || id.Username != "n.sycheva" || id.SID != "sess-1" {
		t.Fatalf("identity: %+v", id)
	}
}

func TestSign_CustomerSessionRoundTrip(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	sess := issuer.Session{
		SID:         "sess-c1",
		Subject:     "customer-account-1",
		Kind:        sso.KindCustomer,
		CompanyID:   "01980000-0000-7000-8000-0000000000a1",
		AccountRole: "admin",
		Products:    []string{"deal"},
	}
	raw, err := s.Sign(sess.Claims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	srv := httptest.NewServer(issuer.JWKSHandler(s))
	t.Cleanup(srv.Close)
	cfg := sso.Config{
		JWKSURL: "https://unused.test/jwks", Cookie: sso.DefaultCookie,
		Issuer: testIssuer, Audience: testAudience, LoginURL: testIssuer,
		Leeway: sso.DefaultLeeway, KeyGrace: sso.DefaultKeyGrace,
		SecuritySchemeName: sso.DefaultSecuritySchemeName,
		CustomerJWKSURL:    srv.URL, CustomerIssuer: testIssuer, CustomerAudience: testAudience,
		CustomerCookie: sso.DefaultCustomerCookie, CustomerLoginURL: testIssuer,
		CustomerSecuritySchemeName: sso.DefaultCustomerSecuritySchemeName,
	}
	v, err := sso.NewCustomerVerifier(cfg, nil)
	if err != nil {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
	id, err := v.Verify(t.Context(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !id.IsCustomer() || id.CompanyID != sess.CompanyID || id.AccountRole != "admin" {
		t.Fatalf("identity: %+v", id)
	}
}

func TestSign_RejectsReservedClaims(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	for _, name := range []string{"iss", "aud", "exp", "nbf", "iat", "typ"} {
		claims := sessionClaims()
		claims[name] = "evil"
		if _, err := s.Sign(claims, time.Minute); !errors.Is(err, issuer.ErrReservedClaim) {
			t.Fatalf("claim %q: err = %v, want ErrReservedClaim", name, err)
		}
	}
}

func TestSign_RequiresStringSubject(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	for name, claims := range map[string]map[string]any{
		"missing":    {"sid": "s1"},
		"not string": {"sub": 123},
		"empty":      {"sub": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Sign(claims, time.Minute); !errors.Is(err, issuer.ErrNoSubject) {
				t.Fatalf("err = %v, want ErrNoSubject", err)
			}
		})
	}
}

func TestSign_RequiresPositiveTTL(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	if _, err := s.Sign(sessionClaims(), 0); err == nil {
		t.Fatal("zero ttl accepted")
	}
}

func TestSign_ClockInjected(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	s := newSigner(t, keyConfig(genPEM(t)), issuer.WithClock(clockkit.NewFixed(at)))
	raw, err := s.Sign(sessionClaims(), time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	inside := verifierFor(t, s, sso.WithClock(clockkit.NewFixed(at.Add(30*time.Minute))))
	id, err := inside.Verify(t.Context(), raw)
	if err != nil {
		t.Fatalf("token within ttl of the injected clock rejected: %v", err)
	}
	if id.Subject != "authentik-sub-1" {
		t.Fatalf("identity: %+v", id)
	}

	past := verifierFor(t, s, sso.WithClock(clockkit.NewFixed(at.Add(2*time.Hour))))
	if _, err := past.Verify(t.Context(), raw); !errors.Is(err, sso.ErrInvalidClaims) {
		t.Fatalf("err = %v, want ErrInvalidClaims (exp считается от часов issuer'а)", err)
	}
}

func TestSign_CustomerRequiresCompanyID(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	sess := issuer.Session{
		SID:         "sess-c2",
		Subject:     "customer-account-2",
		Kind:        sso.KindCustomer,
		AccountRole: "member",
	}
	if _, err := s.Sign(sess.Claims(), time.Minute); !errors.Is(err, issuer.ErrNoCompany) {
		t.Fatalf("err = %v, want ErrNoCompany", err)
	}
	sess.CompanyID = "01980000-0000-7000-8000-0000000000a2"
	if _, err := s.Sign(sess.Claims(), time.Minute); err != nil {
		t.Fatalf("Sign with company_id: %v", err)
	}
}

func TestSign_ForeignKeyRejected(t *testing.T) {
	s := newSigner(t, keyConfig(genPEM(t)))
	other := newSigner(t, keyConfig(genPEM(t)))
	raw, err := other.Sign(sessionClaims(), time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := verifierFor(t, s).Verify(t.Context(), raw); err == nil {
		t.Fatal("token of a foreign key accepted")
	}
}

func TestRotation_PreviousKeyKeepsLiveTokensValid(t *testing.T) {
	oldKey := genKey(t, elliptic.P256())
	before := newSigner(t, keyConfig(privPEM(t, oldKey)))
	live, err := before.Sign(sessionClaims(), 15*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Шаг ротации: active = новый ключ, прежний переезжает в previous-слот
	// (публичный PEM достаточно).
	after := newSigner(t, issuer.KeyConfig{
		ActivePEM:   genPEM(t),
		PreviousPEM: pubPEM(t, oldKey),
		Issuer:      testIssuer,
		Audience:    testAudience,
		JWKSMaxAge:  issuer.DefaultJWKSMaxAge,
	})
	if len(after.JWKS().Keys) != 2 {
		t.Fatalf("JWKS keys = %d, want 2 (active + previous)", len(after.JWKS().Keys))
	}
	if _, err := verifierFor(t, after).Verify(t.Context(), live); err != nil {
		t.Fatalf("token signed by the previous key rejected: %v", err)
	}
}

func TestRotation_NextPublishedBeforeSwitch(t *testing.T) {
	next := genKey(t, elliptic.P256())
	s := newSigner(t, issuer.KeyConfig{
		ActivePEM:  genPEM(t),
		NextPEM:    pubPEM(t, next),
		Issuer:     testIssuer,
		Audience:   testAudience,
		JWKSMaxAge: issuer.DefaultJWKSMaxAge,
	})
	keys := s.JWKS().Keys
	if len(keys) != 2 {
		t.Fatalf("JWKS keys = %d, want 2", len(keys))
	}
	if keys[0].KeyID != s.ActiveKID() {
		t.Fatalf("first key = %q, want active kid", keys[0].KeyID)
	}
	// Токен, выпущенный после переключения, проходит на неизменённом наборе.
	after := newSigner(t, keyConfig(privPEM(t, next)))
	raw, err := after.Sign(sessionClaims(), time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := verifierFor(t, s).Verify(t.Context(), raw); err != nil {
		t.Fatalf("token of the next key rejected: %v", err)
	}
}

func TestNewSigner_KeyFormats(t *testing.T) {
	key := genKey(t, elliptic.P256())
	t.Run("sec1", func(t *testing.T) {
		s := newSigner(t, keyConfig(sec1PEM(t, key)))
		if s.ActiveKID() == "" {
			t.Fatal("no kid")
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "key.pem")
		if err := os.WriteFile(path, []byte(privPEM(t, key)), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}
		s := newSigner(t, issuer.KeyConfig{
			ActiveFile: path,
			ActivePEM:  "ignored",
			Issuer:     testIssuer,
			Audience:   testAudience,
			JWKSMaxAge: issuer.DefaultJWKSMaxAge,
		})
		fromInline := newSigner(t, keyConfig(privPEM(t, key)))
		if s.ActiveKID() != fromInline.ActiveKID() {
			t.Fatal("file key must take priority over inline")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		cfg := keyConfig("")
		cfg.ActiveFile = filepath.Join(t.TempDir(), "absent.pem")
		if _, err := issuer.NewSigner(cfg); err == nil || !strings.Contains(err.Error(), "read key file") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNewSigner_ConfigErrors(t *testing.T) {
	tests := map[string]struct {
		cfg  issuer.KeyConfig
		want error
	}{
		"no key":       {cfg: keyConfig(""), want: issuer.ErrNoActiveKey},
		"bad pem":      {cfg: keyConfig("not a pem"), want: issuer.ErrBadPEM},
		"not p256":     {cfg: keyConfig(privPEM(t, genKey(t, elliptic.P384()))), want: issuer.ErrNotEC},
		"no audience":  {cfg: issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: testIssuer}, want: issuer.ErrConfig},
		"no issuer":    {cfg: issuer.KeyConfig{ActivePEM: genPEM(t), Audience: testAudience}, want: issuer.ErrConfig},
		"issuer path":  {cfg: issuer.KeyConfig{ActivePEM: genPEM(t), Issuer: "id.stit.tech", Audience: testAudience}, want: issuer.ErrConfig},
		"bad next":     {cfg: issuer.KeyConfig{ActivePEM: genPEM(t), NextPEM: "nope", Issuer: testIssuer, Audience: testAudience}, want: issuer.ErrBadPEM},
		"bad previous": {cfg: issuer.KeyConfig{ActivePEM: genPEM(t), PreviousPEM: "nope", Issuer: testIssuer, Audience: testAudience}, want: issuer.ErrBadPEM},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.NewSigner(tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
