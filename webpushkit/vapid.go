package webpushkit

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// vapidTokenTTL — срок жизни VAPID-JWT. RFC 8292 разрешает exp ≤ 24h от now;
// берём с запасом, чтобы часовой перекос клиента/сервиса не отбраковал токен.
const vapidTokenTTL = 12 * time.Hour

// GenerateVAPIDKeys генерирует VAPID-пару P-256/ES256: public — uncompressed
// point (65 байт), private — raw scalar (32 байта), оба base64url без паддинга.
// Пригодится инфре для первичной выдачи WEBPUSH_VAPID_*; public также кладётся
// в браузерный PushManager.subscribe как applicationServerKey.
func GenerateVAPIDKeys() (publicB64, privateB64 string, err error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("webpushkit: generate vapid keys: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(priv.Bytes()), nil
}

// vapidSigner держит подготовленный ES256-ключ и подписывает per-endpoint JWT.
type vapidSigner struct {
	priv    *ecdsa.PrivateKey
	pubB64  string // k= в Authorization: тот же public, что и applicationServerKey
	subject string
}

func newVAPIDSigner(cfg Config) (*vapidSigner, error) {
	if cfg.VAPIDPrivateKey == "" {
		return nil, fmt.Errorf("webpushkit: WEBPUSH_VAPID_PRIVATE_KEY is required: %w", ErrConfig)
	}
	if !strings.HasPrefix(cfg.Subject, "mailto:") && !strings.HasPrefix(cfg.Subject, "https://") {
		return nil, fmt.Errorf("webpushkit: WEBPUSH_SUBJECT must be mailto: or https:// (got %q): %w", cfg.Subject, ErrConfig)
	}
	raw, err := decodeBase64(cfg.VAPIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: decode WEBPUSH_VAPID_PRIVATE_KEY: %w", ErrConfig)
	}
	priv, pubRaw, err := ecdsaFromRawScalar(raw)
	if err != nil {
		return nil, fmt.Errorf("webpushkit: WEBPUSH_VAPID_PRIVATE_KEY not a valid P-256 scalar: %w", ErrConfig)
	}
	// Публичный ключ, если задан, обязан соответствовать приватному: k= в
	// Authorization и applicationServerKey браузера должны быть одним ключом.
	if cfg.VAPIDPublicKey != "" {
		confPub, err := decodeBase64(cfg.VAPIDPublicKey)
		if err != nil {
			return nil, fmt.Errorf("webpushkit: decode WEBPUSH_VAPID_PUBLIC_KEY: %w", ErrConfig)
		}
		if !bytes.Equal(confPub, pubRaw) {
			return nil, fmt.Errorf("webpushkit: WEBPUSH_VAPID_PUBLIC_KEY does not match private key: %w", ErrConfig)
		}
	}
	return &vapidSigner{
		priv:    priv,
		pubB64:  base64.RawURLEncoding.EncodeToString(pubRaw),
		subject: cfg.Subject,
	}, nil
}

// authHeader строит значение заголовка Authorization по RFC 8292 для endpoint'а:
// vapid t=<ES256-JWT{aud=origin, exp, sub}>, k=<public b64url>.
func (s *vapidSigner) authHeader(endpoint string, now time.Time) (string, error) {
	aud, err := origin(endpoint)
	if err != nil {
		return "", err
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": aud,
		"exp": now.Add(vapidTokenTTL).Unix(),
		"sub": s.subject,
	})
	signed, err := tok.SignedString(s.priv)
	if err != nil {
		return "", fmt.Errorf("webpushkit: sign vapid jwt: %w", err)
	}
	return "vapid t=" + signed + ", k=" + s.pubB64, nil
}

// origin — scheme://host endpoint'а: aud VAPID-токена (RFC 8292 §2).
func origin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("webpushkit: parse endpoint: %w", ErrSubscription)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("webpushkit: endpoint must be an absolute URL: %w", ErrSubscription)
	}
	return u.Scheme + "://" + u.Host, nil
}

// ecdsaFromRawScalar восстанавливает ES256-ключ из raw private scalar. Публичную
// точку берём из crypto/ecdh (валидирует scalar и даёт uncompressed-байты), не
// трогая deprecated elliptic.ScalarBaseMult.
func ecdsaFromRawScalar(raw []byte) (priv *ecdsa.PrivateKey, publicRaw []byte, err error) {
	ek, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, nil, err
	}
	pub := ek.PublicKey().Bytes() // 0x04 || X(32) || Y(32)
	priv = &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(pub[1:33]),
			Y:     new(big.Int).SetBytes(pub[33:65]),
		},
		D: new(big.Int).SetBytes(raw),
	}
	return priv, pub, nil
}
