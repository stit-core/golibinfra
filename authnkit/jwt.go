package authnkit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

const (
	// MinSecretLen — не короче выхода SHA-256: более короткий секрет ослабляет HMAC.
	MinSecretLen = 32
)

const (
	// ClaimTokenType — claim с типом токена; проставляется SignAccess и
	// проверяется ParseAccess. Refresh — непрозрачный токен authnkit/session, не JWT.
	ClaimTokenType = "typ"
	// TokenTypeAccess — значение ClaimTokenType у access-токена.
	TokenTypeAccess = "access"
)

// JWTConfig — секреты подписи, срок жизни access-токена, допуск рассинхрона часов и
// привязка токена к выпускающему (iss) и получателям (aud).
type JWTConfig struct {
	Secret string `envconfig:"JWT_SECRET" required:"true"`
	// PreviousSecret — секрет до ротации: подписанные им токены ещё принимаются, новые
	// подписывает Secret; пусто — ротации нет.
	PreviousSecret string        `envconfig:"JWT_PREVIOUS_SECRET" default:""`
	AccessTTL      time.Duration `envconfig:"JWT_ACCESS_TTL" default:"15m"`
	Leeway         time.Duration `envconfig:"JWT_LEEWAY" default:"60s"`
	// Issuer и Audience — iss и aud: Sign* проставляет их, Parse* требует совпадения iss и
	// пересечения aud; пустые не пишутся и не проверяются — тогда сервисы на общем секрете
	// принимают токены друг друга.
	Issuer   string   `envconfig:"JWT_ISSUER" default:""`
	Audience []string `envconfig:"JWT_AUDIENCE" default:""`
}

// Validate требует секреты не короче MinSecretLen, AccessTTL > 0, неотрицательный leeway (0 —
// без допуска) и непустые значения aud; нарушение → ErrConfig.
func (c JWTConfig) Validate() error {
	switch {
	case len(c.Secret) < MinSecretLen:
		return fmt.Errorf("%w: JWT_SECRET must be at least %d bytes", ErrConfig, MinSecretLen)
	case c.PreviousSecret != "" && len(c.PreviousSecret) < MinSecretLen:
		return fmt.Errorf("%w: JWT_PREVIOUS_SECRET must be at least %d bytes", ErrConfig, MinSecretLen)
	case c.AccessTTL <= 0:
		return fmt.Errorf("%w: JWT_ACCESS_TTL must be > 0, got %s", ErrConfig, c.AccessTTL)
	case c.Leeway < 0:
		return fmt.Errorf("%w: JWT_LEEWAY must be >= 0, got %s", ErrConfig, c.Leeway)
	case slices.Contains(c.Audience, ""):
		return fmt.Errorf("%w: JWT_AUDIENCE has an empty value", ErrConfig)
	}
	return nil
}

// JWTSigner — выпуск токенов: с явным TTL и типизированный access.
type JWTSigner interface {
	// Sign — токен с явным ttl и без claim типа.
	Sign(ctx context.Context, claims map[string]any, ttl time.Duration) (string, error)
	// SignAccess — токен с JWTConfig.AccessTTL и typ=access.
	SignAccess(ctx context.Context, claims map[string]any) (string, error)
}

// JWTParser — разбор токенов: без проверки типа и с проверкой access.
type JWTParser interface {
	// Parse — подпись и сроки; тип токена не проверяется.
	Parse(ctx context.Context, token string) (map[string]any, error)
	// ParseAccess — Parse плюс typ=access, иначе ErrWrongTokenType.
	ParseAccess(ctx context.Context, token string) (map[string]any, error)
}

var (
	// ErrTokenExpired — истёк exp (с учётом leeway).
	ErrTokenExpired = errors.New("authnkit: token expired")
	// ErrTokenMalformed — токен нечитаем, alg не HS256, нет exp или nbf/iat в будущем.
	ErrTokenMalformed = errors.New("authnkit: token malformed")
	// ErrTokenSignature — подпись не сошлась.
	ErrTokenSignature = errors.New("authnkit: token signature invalid")
	// ErrWrongTokenType — у токена нет typ=access, который ждёт ParseAccess.
	ErrWrongTokenType = errors.New("authnkit: wrong token type")
	// ErrTokenAudience — iss или aud токена не совпали с ожидаемыми.
	ErrTokenAudience = errors.New("authnkit: token issuer or audience mismatch")
)

// hs256Only — pin алгоритма при разборе: alg none / RS* / другие HMAC-варианты
// отвергаются до проверки подписи.
var hs256Only = []jose.SignatureAlgorithm{jose.HS256}

// JWTOption — опция NewHS256JWT: WithClock.
type JWTOption interface{ applyJWT(*options) }

// TOTPOption — опция NewTOTP: WithClock.
type TOTPOption interface{ applyTOTP(*options) }

// UsedCodeStoreOption — опция NewMemoryUsedCodeStore: WithClock.
type UsedCodeStoreOption interface{ applyUsedCodeStore(*options) }

type options struct {
	clock clockkit.Clock
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
	// keys — секреты проверки: текущий, затем предыдущий (ротация).
	keys      [][]byte
	signer    jose.Signer
	accessTTL time.Duration
	leeway    time.Duration
	issuer    string
	audience  []string
	clock     clockkit.Clock
	instr     *jwtInstruments
}

// NewHS256JWT — подписчик/парсер по cfg; невалидный конфиг → ErrConfig.
func NewHS256JWT(cfg JWTConfig, opts ...JWTOption) (*HS256JWT, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	keys := [][]byte{[]byte(cfg.Secret)}
	if cfg.PreviousSecret != "" {
		keys = append(keys, []byte(cfg.PreviousSecret))
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: keys[0]},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("authnkit: signer: %w", err)
	}
	return &HS256JWT{
		keys:      keys,
		signer:    signer,
		accessTTL: cfg.AccessTTL,
		leeway:    cfg.Leeway,
		issuer:    cfg.Issuer,
		audience:  slices.Clone(cfg.Audience),
		clock:     jwtOptions(opts).clock,
		instr:     newJWTInstruments(),
	}, nil
}

// Sign копирует claims, проставляет exp = now+ttl, iat = now и (если заданы
// конфигом) iss/aud; подпись — текущим секретом. ttl <= 0 → ErrConfig.
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
	all := make(map[string]any, len(claims)+1)
	maps.Copy(all, claims)
	all[ClaimTokenType] = TokenTypeAccess
	return h.Sign(ctx, all, h.accessTTL)
}

// Parse проверяет HS256-подпись (текущим секретом, затем предыдущим), обязательный exp,
// сроки с leeway и заданные конфигом iss/aud. Тип токена не проверяется — для access
// есть ParseAccess.
func (h *HS256JWT) Parse(ctx context.Context, token string) (map[string]any, error) {
	return h.parse(ctx, token, "")
}

// ParseAccess — Parse плюс typ=access; иначе ErrWrongTokenType.
func (h *HS256JWT) ParseAccess(ctx context.Context, token string) (map[string]any, error) {
	return h.parse(ctx, token, TokenTypeAccess)
}

func (h *HS256JWT) parse(ctx context.Context, token, wantType string) (map[string]any, error) {
	tok, err := jwt.ParseSigned(token, hs256Only)
	if err != nil {
		return h.fail(ctx, "malformed", ErrTokenMalformed)
	}

	var std jwt.Claims
	var all map[string]any
	err = jose.ErrCryptoFailure
	for _, key := range h.keys {
		if err = tok.Claims(key, &std, &all); !errors.Is(err, jose.ErrCryptoFailure) {
			break
		}
	}
	if err != nil {
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
