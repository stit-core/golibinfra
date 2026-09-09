package authnkit

import (
	"context"
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

	// DefaultAccessTTL — TTL access-токена при нулевом JWTConfig.AccessTTL.
	DefaultAccessTTL = 15 * time.Minute
	// DefaultRefreshTTL — TTL refresh-токена при нулевом JWTConfig.RefreshTTL.
	DefaultRefreshTTL = 720 * time.Hour
	// DefaultLeeway — допуск рассинхрона часов при нулевом JWTConfig.Leeway.
	DefaultLeeway = time.Minute
)

const (
	// ClaimTokenType — claim с типом токена; проставляется SignAccess/SignRefresh
	// и проверяется ParseAccess/ParseRefresh.
	ClaimTokenType = "typ"
	// TokenTypeAccess — значение ClaimTokenType у access-токена.
	TokenTypeAccess = "access"
	// TokenTypeRefresh — значение ClaimTokenType у refresh-токена.
	TokenTypeRefresh = "refresh"
)

// JWTConfig — секрет, сроки жизни токенов и допуск рассинхрона часов.
type JWTConfig struct {
	Secret     string        `envconfig:"JWT_SECRET" required:"true"`
	AccessTTL  time.Duration `envconfig:"JWT_ACCESS_TTL" default:"15m"`
	RefreshTTL time.Duration `envconfig:"JWT_REFRESH_TTL" default:"720h"`
	Leeway     time.Duration `envconfig:"JWT_LEEWAY" default:"60s"`
}

// Validate требует секрет не короче MinSecretLen и неотрицательные TTL/leeway;
// нарушение → ErrConfig. Нулевые TTL/Leeway означают дефолты кита.
func (c JWTConfig) Validate() error {
	switch {
	case len(c.Secret) < MinSecretLen:
		return fmt.Errorf("%w: JWT secret must be at least %d bytes", ErrConfig, MinSecretLen)
	case c.AccessTTL < 0 || c.RefreshTTL < 0:
		return fmt.Errorf("%w: JWT TTL must not be negative", ErrConfig)
	case c.Leeway < 0:
		return fmt.Errorf("%w: JWT leeway must not be negative", ErrConfig)
	}
	return nil
}

// JWTSigner — выпуск токенов: с явным TTL и типизированные access/refresh.
type JWTSigner interface {
	// Sign — токен с явным ttl и без claim типа.
	Sign(ctx context.Context, claims map[string]any, ttl time.Duration) (string, error)
	// SignAccess — токен с JWTConfig.AccessTTL и typ=access.
	SignAccess(ctx context.Context, claims map[string]any) (string, error)
	// SignRefresh — токен с JWTConfig.RefreshTTL и typ=refresh.
	SignRefresh(ctx context.Context, claims map[string]any) (string, error)
}

// JWTParser — разбор токенов: без проверки типа и с проверкой access/refresh.
type JWTParser interface {
	// Parse — подпись и сроки; тип токена не проверяется.
	Parse(ctx context.Context, token string) (map[string]any, error)
	// ParseAccess — Parse плюс typ=access, иначе ErrWrongTokenType.
	ParseAccess(ctx context.Context, token string) (map[string]any, error)
	// ParseRefresh — Parse плюс typ=refresh, иначе ErrWrongTokenType.
	ParseRefresh(ctx context.Context, token string) (map[string]any, error)
}

var (
	// ErrTokenExpired — истёк exp (с учётом leeway).
	ErrTokenExpired = errors.New("authnkit: token expired")
	// ErrTokenMalformed — токен нечитаем, alg не HS256, нет exp или nbf/iat в будущем.
	ErrTokenMalformed = errors.New("authnkit: token malformed")
	// ErrTokenSignature — подпись не сошлась.
	ErrTokenSignature = errors.New("authnkit: token signature invalid")
	// ErrWrongTokenType — тип токена не тот, который ждёт ParseAccess/ParseRefresh.
	ErrWrongTokenType = errors.New("authnkit: wrong token type")
	// ErrTokenAudience — iss или aud токена не совпали с ожидаемыми.
	ErrTokenAudience = errors.New("authnkit: token issuer or audience mismatch")
)

// hs256Only — pin алгоритма при разборе: alg none / RS* / другие HMAC-варианты
// отвергаются до проверки подписи.
var hs256Only = []jose.SignatureAlgorithm{jose.HS256}

// JWTOption — опция NewHS256JWT: WithClock, WithIssuer, WithAudience.
type JWTOption interface{ applyJWT(*options) }

// TOTPOption — опция NewTOTP: WithClock.
type TOTPOption interface{ applyTOTP(*options) }

// UsedCodeStoreOption — опция NewMemoryUsedCodeStore: WithClock.
type UsedCodeStoreOption interface{ applyUsedCodeStore(*options) }

type options struct {
	clock    clockkit.Clock
	issuer   string
	audience []string
}

// ClockOption — подмена часов; принимается всеми конструкторами кита.
type ClockOption struct{ clock clockkit.Clock }

// WithClock подменяет часы (NewHS256JWT, NewTOTP, NewMemoryUsedCodeStore);
// nil оставляет clockkit.Real.
func WithClock(c clockkit.Clock) ClockOption { return ClockOption{clock: c} }

func (o ClockOption) apply(dst *options) {
	if o.clock != nil {
		dst.clock = o.clock
	}
}

func (o ClockOption) applyJWT(dst *options)           { o.apply(dst) }
func (o ClockOption) applyTOTP(dst *options)          { o.apply(dst) }
func (o ClockOption) applyUsedCodeStore(dst *options) { o.apply(dst) }

// jwtOption — опция, применимая только к NewHS256JWT: тип, а не пометка в
// doc-комментарии, чтобы iss/aud нельзя было молча передать в NewTOTP.
type jwtOption func(*options)

func (f jwtOption) applyJWT(dst *options) { f(dst) }

// WithIssuer — iss токена: Sign* проставляет его, Parse* требует совпадения.
func WithIssuer(issuer string) JWTOption {
	return jwtOption(func(o *options) { o.issuer = issuer })
}

// WithAudience — aud токена: Sign* проставляет его, Parse* требует пересечения
// с ним — токен соседнего сервиса на общем секрете не пройдёт.
func WithAudience(audience ...string) JWTOption {
	return jwtOption(func(o *options) { o.audience = audience })
}

func defaultOptions() options { return options{clock: clockkit.Real{}} }

func jwtOptions(opts []JWTOption) options {
	o := defaultOptions()
	for _, opt := range opts {
		opt.applyJWT(&o)
	}
	return o
}

func totpOptions(opts []TOTPOption) options {
	o := defaultOptions()
	for _, opt := range opts {
		opt.applyTOTP(&o)
	}
	return o
}

func usedCodeStoreOptions(opts []UsedCodeStoreOption) options {
	o := defaultOptions()
	for _, opt := range opts {
		opt.applyUsedCodeStore(&o)
	}
	return o
}

// HS256JWT — JWTSigner и JWTParser на HS256 поверх go-jose.
type HS256JWT struct {
	secret     []byte
	signer     jose.Signer
	accessTTL  time.Duration
	refreshTTL time.Duration
	leeway     time.Duration
	issuer     string
	audience   []string
	clock      clockkit.Clock
	instr      *jwtInstruments
}

// NewHS256JWT — подписчик/парсер по cfg; невалидный конфиг → ErrConfig.
func NewHS256JWT(cfg JWTConfig, opts ...JWTOption) (*HS256JWT, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret := []byte(cfg.Secret)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: secret},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("authnkit: signer: %w", err)
	}
	o := jwtOptions(opts)
	return &HS256JWT{
		secret:     secret,
		signer:     signer,
		accessTTL:  durationOr(cfg.AccessTTL, DefaultAccessTTL),
		refreshTTL: durationOr(cfg.RefreshTTL, DefaultRefreshTTL),
		leeway:     durationOr(cfg.Leeway, DefaultLeeway),
		issuer:     o.issuer,
		audience:   o.audience,
		clock:      o.clock,
		instr:      newJWTInstruments(),
	}, nil
}

func durationOr(d, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return d
}

// Sign копирует claims, проставляет exp = now+ttl, iat = now и (если заданы
// опциями) iss/aud. ttl <= 0 → ErrConfig.
func (h *HS256JWT) Sign(ctx context.Context, claims map[string]any, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("%w: token ttl must be > 0", ErrConfig)
	}
	now := h.clock.Now()
	all := make(map[string]any, len(claims)+4)
	maps.Copy(all, claims)
	all["iat"] = now.Unix()
	all["exp"] = now.Add(ttl).Unix()
	if h.issuer != "" {
		all["iss"] = h.issuer
	}
	switch len(h.audience) {
	case 0:
	case 1:
		all["aud"] = h.audience[0]
	default:
		all["aud"] = h.audience
	}

	signed, err := jwt.Signed(h.signer).Claims(all).Serialize()
	if err != nil {
		return "", fmt.Errorf("authnkit: sign: %w", err)
	}
	return signed, nil
}

// SignAccess — токен с JWTConfig.AccessTTL и typ=access.
func (h *HS256JWT) SignAccess(ctx context.Context, claims map[string]any) (string, error) {
	return h.signTyped(ctx, claims, TokenTypeAccess, h.accessTTL)
}

// SignRefresh — токен с JWTConfig.RefreshTTL и typ=refresh.
func (h *HS256JWT) SignRefresh(ctx context.Context, claims map[string]any) (string, error) {
	return h.signTyped(ctx, claims, TokenTypeRefresh, h.refreshTTL)
}

func (h *HS256JWT) signTyped(ctx context.Context, claims map[string]any, typ string, ttl time.Duration) (string, error) {
	all := make(map[string]any, len(claims)+1)
	maps.Copy(all, claims)
	all[ClaimTokenType] = typ
	return h.Sign(ctx, all, ttl)
}

// Parse проверяет HS256-подпись, обязательный exp, сроки с leeway и заданные
// опциями iss/aud. Тип токена не проверяется — для access/refresh есть
// ParseAccess/ParseRefresh.
func (h *HS256JWT) Parse(ctx context.Context, token string) (map[string]any, error) {
	return h.parse(ctx, token, "")
}

// ParseAccess — Parse плюс typ=access; иначе ErrWrongTokenType.
func (h *HS256JWT) ParseAccess(ctx context.Context, token string) (map[string]any, error) {
	return h.parse(ctx, token, TokenTypeAccess)
}

// ParseRefresh — Parse плюс typ=refresh; иначе ErrWrongTokenType.
func (h *HS256JWT) ParseRefresh(ctx context.Context, token string) (map[string]any, error) {
	return h.parse(ctx, token, TokenTypeRefresh)
}

func (h *HS256JWT) parse(ctx context.Context, token, wantType string) (map[string]any, error) {
	tok, err := jwt.ParseSigned(token, hs256Only)
	if err != nil {
		return h.fail(ctx, "malformed", ErrTokenMalformed)
	}

	var std jwt.Claims
	var all map[string]any
	if err := tok.Claims(h.secret, &std, &all); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return h.fail(ctx, "signature_invalid", ErrTokenSignature)
		}
		return h.fail(ctx, "malformed", ErrTokenMalformed)
	}
	if std.Expiry == nil {
		return h.fail(ctx, "malformed", ErrTokenMalformed)
	}
	expected := jwt.Expected{Time: h.clock.Now(), Issuer: h.issuer, AnyAudience: h.audience}
	if err := std.ValidateWithLeeway(expected, h.leeway); err != nil {
		switch {
		case errors.Is(err, jwt.ErrExpired):
			return h.fail(ctx, "expired", ErrTokenExpired)
		case errors.Is(err, jwt.ErrInvalidIssuer), errors.Is(err, jwt.ErrInvalidAudience):
			return h.fail(ctx, "audience_mismatch", ErrTokenAudience)
		default:
			return h.fail(ctx, "malformed", ErrTokenMalformed)
		}
	}
	if wantType != "" {
		if typ, _ := all[ClaimTokenType].(string); typ != wantType {
			return h.fail(ctx, "wrong_type", ErrWrongTokenType)
		}
	}
	h.instr.recordJWT(ctx, "ok")
	return all, nil
}

func (h *HS256JWT) fail(ctx context.Context, result string, err error) (map[string]any, error) {
	h.instr.recordJWT(ctx, result)
	return nil, err
}
