package authnkit

import (
	"errors"
	"fmt"
	"maps"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const (
	// MinSecretLen — не короче выхода SHA-256: более короткий секрет ослабляет HMAC.
	MinSecretLen = 32

	DefaultAccessTTL  = 15 * time.Minute
	DefaultRefreshTTL = 720 * time.Hour
	DefaultLeeway     = time.Minute
)

type JWTConfig struct {
	Secret     string        `envconfig:"JWT_SECRET" required:"true"`
	AccessTTL  time.Duration `envconfig:"JWT_ACCESS_TTL" default:"15m"`
	RefreshTTL time.Duration `envconfig:"JWT_REFRESH_TTL" default:"720h"`
	Leeway     time.Duration `envconfig:"JWT_LEEWAY" default:"60s"`
}

type JWTSigner interface {
	Sign(claims map[string]any, ttl time.Duration) (string, error)
}

type JWTParser interface {
	Parse(token string) (map[string]any, error)
}

var (
	ErrTokenExpired   = errors.New("authnkit: token expired")
	ErrTokenMalformed = errors.New("authnkit: token malformed")
	ErrTokenSignature = errors.New("authnkit: token signature invalid")
)

// hs256Only — pin алгоритма при разборе: alg none / RS* / другие HMAC-варианты
// отвергаются до проверки подписи.
var hs256Only = []jose.SignatureAlgorithm{jose.HS256}

type Option func(*options)

type options struct {
	clock clockkit.Clock
}

func WithClock(c clockkit.Clock) Option {
	return func(o *options) { o.clock = c }
}

func applyOptions(opts []Option) options {
	o := options{clock: clockkit.Real{}}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

type HS256JWT struct {
	secret     []byte
	signer     jose.Signer
	accessTTL  time.Duration
	refreshTTL time.Duration
	leeway     time.Duration
	clock      clockkit.Clock
	instr      *instruments
}

// NewHS256JWT: секрет короче MinSecretLen или отрицательные TTL/Leeway →
// ErrConfig; нулевые TTL/Leeway получают дефолты.
func NewHS256JWT(cfg JWTConfig, opts ...Option) (*HS256JWT, error) {
	switch {
	case len(cfg.Secret) < MinSecretLen:
		return nil, fmt.Errorf("%w: JWT secret must be at least %d bytes", ErrConfig, MinSecretLen)
	case cfg.AccessTTL < 0 || cfg.RefreshTTL < 0:
		return nil, fmt.Errorf("%w: JWT TTL must be positive", ErrConfig)
	case cfg.Leeway < 0:
		return nil, fmt.Errorf("%w: JWT leeway must not be negative", ErrConfig)
	}
	secret := []byte(cfg.Secret)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: secret},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("authnkit: signer: %w", err)
	}
	o := applyOptions(opts)
	return &HS256JWT{
		secret:     secret,
		signer:     signer,
		accessTTL:  durationOr(cfg.AccessTTL, DefaultAccessTTL),
		refreshTTL: durationOr(cfg.RefreshTTL, DefaultRefreshTTL),
		leeway:     durationOr(cfg.Leeway, DefaultLeeway),
		clock:      o.clock,
		instr:      newInstruments(),
	}, nil
}

func durationOr(d, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return d
}

// Sign копирует claims, проставляет exp = now+ttl и iat = now.
func (h *HS256JWT) Sign(claims map[string]any, ttl time.Duration) (string, error) {
	now := h.clock.Now()
	all := make(map[string]any, len(claims)+2)
	maps.Copy(all, claims)
	all["iat"] = now.Unix()
	all["exp"] = now.Add(ttl).Unix()

	signed, err := jwt.Signed(h.signer).Claims(all).Serialize()
	if err != nil {
		return "", fmt.Errorf("authnkit: sign: %w", err)
	}
	return signed, nil
}

// SignAccess — Sign с JWTConfig.AccessTTL.
func (h *HS256JWT) SignAccess(claims map[string]any) (string, error) {
	return h.Sign(claims, h.accessTTL)
}

// SignRefresh — Sign с JWTConfig.RefreshTTL.
func (h *HS256JWT) SignRefresh(claims map[string]any) (string, error) {
	return h.Sign(claims, h.refreshTTL)
}

// Parse проверяет HS256-подпись, обязательный exp и exp/nbf/iat с leeway.
func (h *HS256JWT) Parse(token string) (map[string]any, error) {
	tok, err := jwt.ParseSigned(token, hs256Only)
	if err != nil {
		return h.fail("malformed", ErrTokenMalformed)
	}

	var std jwt.Claims
	var all map[string]any
	if err := tok.Claims(h.secret, &std, &all); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return h.fail("signature_invalid", ErrTokenSignature)
		}
		return h.fail("malformed", ErrTokenMalformed)
	}
	if std.Expiry == nil {
		return h.fail("malformed", ErrTokenMalformed)
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Time: h.clock.Now()}, h.leeway); err != nil {
		if errors.Is(err, jwt.ErrExpired) {
			return h.fail("expired", ErrTokenExpired)
		}
		return h.fail("malformed", ErrTokenMalformed)
	}
	h.instr.recordJWT("ok")
	return all, nil
}

func (h *HS256JWT) fail(result string, err error) (map[string]any, error) {
	h.instr.recordJWT(result)
	return nil, err
}
