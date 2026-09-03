// Package issuer — выпуск hub-сессии issuer'ами контуров (employee-backend,
// company-backend): ES256-подпись session-JWT с ротацией ключа (активный +
// опциональный next в JWKS), cookie-слой и rotating-refresh поверх
// cachekit.Cache с reuse-detection и tombstone на логаут. Верификатор этих
// токенов — hubkit/sso (тот же контракт claim'ов).
package issuer

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

var (
	ErrNoActiveKey = errors.New("hubkit/issuer: active signing key required")
	ErrNoAudience  = errors.New("hubkit/issuer: HUB_SESSION_AUDIENCE required")
	ErrBadPEM      = errors.New("hubkit/issuer: not a PEM-encoded key")
	ErrNotEC       = errors.New("hubkit/issuer: key is not an ECDSA P-256 key")
)

// signKey — ключ подписи: приватный ECDSA + jose-signer + публичный JWK с kid
// (JWK thumbprint RFC 7638).
type signKey struct {
	kid    string
	signer jose.Signer
	pubJWK jose.JSONWebKey
}

func newSignKey(priv *ecdsa.PrivateKey) (signKey, error) {
	pub, err := publicJWK(&priv.PublicKey)
	if err != nil {
		return signKey{}, err
	}
	s, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", pub.KeyID).WithType("JWT"),
	)
	if err != nil {
		return signKey{}, fmt.Errorf("hubkit/issuer: signer: %w", err)
	}
	return signKey{kid: pub.KeyID, signer: s, pubJWK: pub}, nil
}

func publicJWK(pub *ecdsa.PublicKey) (jose.JSONWebKey, error) {
	jwk := jose.JSONWebKey{Key: pub, Algorithm: string(jose.ES256), Use: "sig"}
	tp, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return jose.JSONWebKey{}, fmt.Errorf("hubkit/issuer: thumbprint: %w", err)
	}
	jwk.KeyID = base64.RawURLEncoding.EncodeToString(tp)
	return jwk, nil
}

// Signer подписывает session-JWT активным ключом и публикует JWKS (активный +
// next). kid активного ключа кладётся в заголовок токена — sso-verifier по нему
// выбирает публичный ключ.
type Signer struct {
	active   signKey
	next     *jose.JSONWebKey
	issuer   string
	audience string
}

// NewSigner: активный ключ — приватный P-256 (SEC1 или PKCS#8); next — тот же
// формат либо публичный PKIX PEM (приватный ключ следующего kid инстансу не нужен).
func NewSigner(cfg KeyConfig) (*Signer, error) {
	if cfg.Audience == "" {
		return nil, ErrNoAudience
	}
	activePEM, err := cfg.activePEM()
	if err != nil {
		return nil, err
	}
	if activePEM == "" {
		return nil, ErrNoActiveKey
	}
	activeKey, err := parseECPrivateKey(activePEM)
	if err != nil {
		return nil, err
	}
	active, err := newSignKey(activeKey)
	if err != nil {
		return nil, err
	}
	s := &Signer{active: active, issuer: cfg.Issuer, audience: cfg.Audience}

	nextPEM, err := cfg.nextPEM()
	if err != nil {
		return nil, err
	}
	if nextPEM != "" {
		nextPub, err := parseECPublicKey(nextPEM)
		if err != nil {
			return nil, err
		}
		nk, err := publicJWK(nextPub)
		if err != nil {
			return nil, err
		}
		s.next = &nk
	}
	return s, nil
}

// ActiveKID — kid ключа, которым подписываются свежие токены.
func (s *Signer) ActiveKID() string { return s.active.kid }

// Sign подписывает claims активным ключом, добавляя iss/aud и exp/iat/nbf.
// sub кладётся в claims caller'ом.
func (s *Signer) Sign(claims map[string]any, ttl time.Duration) (string, error) {
	now := time.Now()
	std := jwt.Claims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(now.Add(ttl)),
		Audience:  jwt.Audience{s.audience},
	}
	if s.issuer != "" {
		std.Issuer = s.issuer
	}
	if sub, ok := claims["sub"].(string); ok {
		std.Subject = sub
	}

	builder := jwt.Signed(s.active.signer).Claims(std)
	custom := make(map[string]any, len(claims))
	for k, v := range claims {
		if k == "sub" {
			continue
		}
		custom[k] = v
	}
	if len(custom) > 0 {
		builder = builder.Claims(custom)
	}
	raw, err := builder.Serialize()
	if err != nil {
		return "", fmt.Errorf("hubkit/issuer: sign: %w", err)
	}
	return raw, nil
}

// JWKS — публичный набор ключей (активный + next), который отдаёт
// /.well-known/jwks.json. sso-verifier принимает подпись любым из них.
func (s *Signer) JWKS() jose.JSONWebKeySet {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{s.active.pubJWK}}
	if s.next != nil {
		set.Keys = append(set.Keys, *s.next)
	}
	return set
}

func parseECPrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, ErrBadPEM
	}
	// Поддерживаем оба контейнера: SEC1 ("EC PRIVATE KEY") и PKCS#8 ("PRIVATE KEY").
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("hubkit/issuer: parse ec key: %w", err)
		}
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, ErrNotEC
		}
		key = ec
	}
	if key.Curve != elliptic.P256() {
		return nil, ErrNotEC
	}
	return key, nil
}

func parseECPublicKey(pemStr string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, ErrBadPEM
	}
	if block.Type != "PUBLIC KEY" {
		priv, err := parseECPrivateKey(pemStr)
		if err != nil {
			return nil, err
		}
		return &priv.PublicKey, nil
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("hubkit/issuer: parse ec public key: %w", err)
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrNotEC
	}
	return pub, nil
}
