// Package hubtest — тестовый issuer hub-сессии: ES256-ключи, JWKS-сервер,
// токены обоих контуров и готовые verifier'ы/extractor для тестов продуктов.
package hubtest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Значения по умолчанию, которые issuer кладёт в выдаваемые токены.
const (
	DefaultIssuerURL         = "https://id.test"
	DefaultAudience          = "stit"
	DefaultCustomerIssuerURL = "https://api.hub.test"
	DefaultCustomerAudience  = "stit-customer"
	DefaultSubject           = "authentik-subject-1"
	DefaultEmployeeID        = "01980000-0000-7000-8000-000000000001"
	DefaultUsername          = "j.doe"
	DefaultName              = "Jane Doe"
	DefaultEmail             = "j.doe@stit.test"
	DefaultRole              = "employee"
	DefaultSID               = "session-1"
	DefaultCustomerSubject   = "01980000-0000-7000-8000-0000000000c1"
	DefaultCompanyID         = "01980000-0000-7000-8000-0000000000a1"
	DefaultAccountRole       = "member"
	DefaultTokenTTL          = 15 * time.Minute
)

// Option — настройка тестового issuer'а, передаётся в NewIssuer.
type Option func(*Issuer)

// WithIssuerURL задаёт iss выпускаемых токенов (default DefaultIssuerURL).
func WithIssuerURL(iss string) Option { return func(i *Issuer) { i.issuer = iss } }

// WithAudience задаёт aud выпускаемых токенов (default DefaultAudience).
func WithAudience(aud string) Option { return func(i *Issuer) { i.audience = aud } }

// Issuer — тестовый issuer hub-сессии: ES256-ключ с kid по RFC 7638 и
// httptest-сервер JWKS. Сервер и ключи снимаются в t.Cleanup.
type Issuer struct {
	issuer   string
	audience string

	mu     sync.RWMutex
	priv   *ecdsa.PrivateKey
	kid    string
	signer jose.Signer
	keys   []jose.JSONWebKey

	srv  *httptest.Server
	hits atomic.Int64
}

// NewIssuer поднимает issuer сотруднического контура с JWKS-сервером.
func NewIssuer(t testing.TB, opts ...Option) *Issuer {
	t.Helper()
	i := &Issuer{issuer: DefaultIssuerURL, audience: DefaultAudience}
	for _, apply := range opts {
		apply(i)
	}
	i.rotate(t, false)
	i.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(i.jwks())
	}))
	t.Cleanup(i.srv.Close)
	return i
}

// NewCustomerIssuer — issuer контура заказчика: те же ключи и сервер, но iss/aud
// по умолчанию его контура.
func NewCustomerIssuer(t testing.TB, opts ...Option) *Issuer {
	t.Helper()
	base := make([]Option, 0, 2+len(opts))
	base = append(base, WithIssuerURL(DefaultCustomerIssuerURL), WithAudience(DefaultCustomerAudience))
	return NewIssuer(t, append(base, opts...)...)
}

// JWKSURL — адрес JWKS-сервера (значение для HUB_SSO_JWKS_URL).
func (i *Issuer) JWKSURL() string { return i.srv.URL }

// IssuerURL — iss выпускаемых токенов.
func (i *Issuer) IssuerURL() string { return i.issuer }

// Audience — aud выпускаемых токенов.
func (i *Issuer) Audience() string { return i.audience }

// KID — kid активного ключа подписи.
func (i *Issuer) KID() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.kid
}

// JWKSRequests — сколько раз verifier ходил за набором ключей.
func (i *Issuer) JWKSRequests() int64 { return i.hits.Load() }

// Rotate генерирует новый активный ключ; прежний исчезает из JWKS — так
// выглядит ротация, забывшая про previous-слот.
func (i *Issuer) Rotate(t testing.TB) { t.Helper(); i.rotate(t, false) }

// RotateKeepingPrevious генерирует новый активный ключ и оставляет прежний в
// наборе — штатная ротация с previous-слотом.
func (i *Issuer) RotateKeepingPrevious(t testing.TB) { t.Helper(); i.rotate(t, true) }

func (i *Issuer) rotate(t testing.TB, keepPrevious bool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("hubtest: generate key: %v", err)
	}
	pub := jose.JSONWebKey{Key: priv.Public(), Algorithm: string(jose.ES256), Use: "sig"}
	tp, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatalf("hubtest: thumbprint: %v", err)
	}
	pub.KeyID = base64.RawURLEncoding.EncodeToString(tp)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", pub.KeyID).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("hubtest: signer: %v", err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	keys := []jose.JSONWebKey{pub}
	if keepPrevious {
		keys = append(keys, i.keys...)
	}
	i.priv, i.kid, i.signer, i.keys = priv, pub.KeyID, signer, keys
}

func (i *Issuer) jwks() jose.JSONWebKeySet {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), i.keys...)}
}

// JWKS — текущий публичный набор ключей issuer'а.
func (i *Issuer) JWKS() jose.JSONWebKeySet { return i.jwks() }

// PublicKey — публичный ключ активного kid (для негативных тестов подписи).
func (i *Issuer) PublicKey() *ecdsa.PublicKey {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return &i.priv.PublicKey
}
