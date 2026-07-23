// Package issuer — выпуск hub-сессии (используется только employee-backend,
// id.stit.tech).
//
// ES256-подпись session-JWT приватным ключом хаба с поддержкой ротации
// (активный ключ + опциональный next), публикация JWKS (оба ключа на период
// ротации, чтобы старые токены оставались валидны), cookie-слой
// (Domain=.stit.tech, HttpOnly, Secure, SameSite=Lax, короткий TTL) и
// rotating-refresh поверх Redis с tombstone на логаут.
//
// Верификатор этих токенов — hubkit/sso (тот же контракт claim'ов); employee-backend
// защищает и собственные ручки через sso-extractor (dogfooding).
package issuer

import (
	"crypto"
	"crypto/ecdsa"
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
	ErrBadPEM      = errors.New("hubkit/issuer: not a PEM-encoded key")
	ErrNotEC       = errors.New("hubkit/issuer: key is not an ECDSA P-256 private key")
)

// signKey — один ключ подписи: приватный ECDSA + производный kid (JWK
// thumbprint RFC 7638) + jose-signer.
type signKey struct {
	kid    string
	priv   *ecdsa.PrivateKey
	signer jose.Signer
	pubJWK jose.JSONWebKey
}

func newSignKey(priv *ecdsa.PrivateKey) (signKey, error) {
	pub := jose.JSONWebKey{Key: priv.Public(), Algorithm: string(jose.ES256), Use: "sig"}
	tp, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return signKey{}, fmt.Errorf("hubkit/issuer: thumbprint: %w", err)
	}
	kid := base64.RawURLEncoding.EncodeToString(tp)
	pub.KeyID = kid

	s, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: priv},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		return signKey{}, fmt.Errorf("hubkit/issuer: signer: %w", err)
	}
	return signKey{kid: kid, priv: priv, signer: s, pubJWK: pub}, nil
}

// Signer подписывает session-JWT активным ключом и публикует JWKS (активный +
// next). kid активного ключа кладётся в заголовок токена — sso-verifier по нему
// выбирает публичный ключ.
type Signer struct {
	active   signKey
	next     *signKey
	issuer   string
	audience string
}

// NewSigner строит Signer из уже загруженных PEM-ключей. nextPEM пуст — ротации нет.
func NewSigner(cfg KeyConfig) (*Signer, error) {
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
		nextKey, err := parseECPrivateKey(nextPEM)
		if err != nil {
			return nil, err
		}
		nk, err := newSignKey(nextKey)
		if err != nil {
			return nil, err
		}
		s.next = &nk
	}
	return s, nil
}

// ActiveKID — kid ключа, которым подписываются свежие токены.
func (s *Signer) ActiveKID() string { return s.active.kid }

// Sign подписывает claims активным ключом, добавляя iss/aud (если заданы),
// exp/iat/nbf. sub кладётся в claims caller'ом (обычно через SessionClaims).
func (s *Signer) Sign(claims map[string]any, ttl time.Duration) (string, error) {
	now := time.Now()
	std := jwt.Claims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(now.Add(ttl)),
	}
	if s.issuer != "" {
		std.Issuer = s.issuer
	}
	if s.audience != "" {
		std.Audience = jwt.Audience{s.audience}
	}
	if sub, ok := claims["sub"].(string); ok {
		std.Subject = sub
	}

	builder := jwt.Signed(s.active.signer).Claims(std)
	// Пользовательские claims поверх стандартных (sub уже в std — не дублируем).
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
		set.Keys = append(set.Keys, s.next.pubJWK)
	}
	return set
}

func parseECPrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, ErrBadPEM
	}
	// Поддерживаем оба контейнера: SEC1 ("EC PRIVATE KEY") и PKCS#8 ("PRIVATE KEY").
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("hubkit/issuer: parse ec key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, ErrNotEC
	}
	return ec, nil
}
