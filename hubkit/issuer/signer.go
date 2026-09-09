// Package issuer — выпуск hub-сессии: ES256-подпись session-JWT с ротацией
// ключа, JWKS-роут, cookie-слой и rotating-refresh поверх cachekit.Cache.
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
	"slices"
	"sort"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/hubclaims"
)

var (
	// ErrConfig — невалидный конфиг issuer'а (Validate любого из трёх конфигов).
	ErrConfig = errors.New("hubkit/issuer: invalid config")
	// ErrNoActiveKey — не задан активный ключ подписи.
	ErrNoActiveKey = fmt.Errorf("%w: HUB_SESSION_PRIVATE_KEY(_FILE) is required", ErrConfig)
	// ErrBadPEM — значение ключа не является PEM.
	ErrBadPEM = errors.New("hubkit/issuer: not a PEM-encoded key")
	// ErrNotEC — ключ не ECDSA P-256 (единственная поддерживаемая кривая).
	ErrNotEC = errors.New("hubkit/issuer: key is not an ECDSA P-256 key")
	// ErrReservedClaim — caller передал claim, который выставляет сам issuer.
	ErrReservedClaim = errors.New("hubkit/issuer: reserved claim cannot be set by the caller")
	// ErrNoSubject — в claims нет непустого строкового sub.
	ErrNoSubject = errors.New("hubkit/issuer: claims must carry a non-empty string sub")
)

// reservedClaims — claims, которые выставляет issuer: подмена любого из них
// caller'ом означала бы токен на чужой aud или без срока.
var reservedClaims = []string{"iss", "aud", "exp", "nbf", "iat", "typ"}

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

// Option — настройка Signer и RefreshManager, передаётся в конструктор.
type Option func(*options)

type options struct {
	clock clockkit.Clock
}

// WithClock подменяет часы выпуска токенов и grace-окна refresh'а
// (детерминированные тесты).
func WithClock(clock clockkit.Clock) Option {
	return func(o *options) {
		if clock != nil {
			o.clock = clock
		}
	}
}

// Signer подписывает session-JWT активным ключом и публикует JWKS (активный +
// next + previous). kid активного ключа кладётся в заголовок токена —
// sso-verifier по нему выбирает публичный ключ.
type Signer struct {
	active     signKey
	next       *jose.JSONWebKey
	previous   *jose.JSONWebKey
	issuer     string
	audience   string
	jwksMaxAge time.Duration
	clock      clockkit.Clock
}

// NewSigner собирает подписчик из KeyConfig: активный ключ — приватный P-256
// (SEC1 или PKCS#8); next и previous — тот же формат либо публичный PKIX PEM
// (приватный ключ соседнего kid инстансу не нужен).
func NewSigner(cfg KeyConfig, opts ...Option) (*Signer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := options{clock: clockkit.Real{}}
	for _, apply := range opts {
		apply(&o)
	}
	activePEM, err := cfg.activePEM()
	if err != nil {
		return nil, err
	}
	activeKey, err := parseECPrivateKey(activePEM)
	if err != nil {
		return nil, err
	}
	active, err := newSignKey(activeKey)
	if err != nil {
		return nil, err
	}
	s := &Signer{
		active:     active,
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		jwksMaxAge: cfg.jwksMaxAge(),
		clock:      o.clock,
	}
	if s.next, err = publicSlot(cfg.nextPEM); err != nil {
		return nil, err
	}
	if s.previous, err = publicSlot(cfg.previousPEM); err != nil {
		return nil, err
	}
	return s, nil
}

func publicSlot(read func() (string, error)) (*jose.JSONWebKey, error) {
	raw, err := read()
	if err != nil || raw == "" {
		return nil, err
	}
	pub, err := parseECPublicKey(raw)
	if err != nil {
		return nil, err
	}
	jwk, err := publicJWK(pub)
	if err != nil {
		return nil, err
	}
	return &jwk, nil
}

// ActiveKID — kid ключа, которым подписываются свежие токены.
func (s *Signer) ActiveKID() string { return s.active.kid }

// JWKSMaxAge — окно кэширования JWKS-ответа (Cache-Control: max-age).
func (s *Signer) JWKSMaxAge() time.Duration { return s.jwksMaxAge }

// Sign подписывает claims активным ключом, выставляя iss/aud/exp/iat/nbf. sub —
// обязательный непустой строковый claim caller'а; попытка задать любой из
// reservedClaims — ErrReservedClaim (иначе одно лишнее поле карты выпустило бы
// токен на чужой aud или без срока). Customer-токен без company_id —
// ErrNoCompany: продукт отвергает такой токен, и диагностика всплывала бы у
// пользователя, а не на стороне выпуска.
func (s *Signer) Sign(claims map[string]any, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("hubkit/issuer: sign: ttl must be > 0")
	}
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", ErrNoSubject
	}
	if bad := reservedIn(claims); bad != "" {
		return "", fmt.Errorf("%w: %s", ErrReservedClaim, bad)
	}
	if kind, _ := claims[hubclaims.Kind].(string); kind == hubclaims.KindCustomer {
		if company, _ := claims[hubclaims.CompanyID].(string); company == "" {
			return "", ErrNoCompany
		}
	}

	now := s.clock.Now()
	std := jwt.Claims{
		Issuer:    s.issuer,
		Subject:   sub,
		Audience:  jwt.Audience{s.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(now.Add(ttl)),
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

func reservedIn(claims map[string]any) string {
	found := make([]string, 0, len(reservedClaims))
	for k := range claims {
		if slices.Contains(reservedClaims, strings.ToLower(k)) {
			found = append(found, k)
		}
	}
	sort.Strings(found)
	return strings.Join(found, ", ")
}

// JWKS — публичный набор ключей (активный + next + previous), который отдаёт
// /.well-known/jwks.json. sso-verifier принимает подпись любым из них.
func (s *Signer) JWKS() jose.JSONWebKeySet {
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{s.active.pubJWK}}
	for _, k := range []*jose.JSONWebKey{s.next, s.previous} {
		if k != nil && k.KeyID != s.active.kid {
			set.Keys = append(set.Keys, *k)
		}
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
