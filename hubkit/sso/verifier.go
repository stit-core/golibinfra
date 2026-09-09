// Package sso — верификация hub-session JWT продуктом: ES256/JWKS двух контуров
// (сотрудник и заказчик), Identity в ctx, extractor и security-схемы для httpkit.
package sso

import (
	"context"
	"errors"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/hubclaims"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

var (
	// ErrConfig — невалидный конфиг верификатора (Config.Validate).
	ErrConfig = errors.New("hubkit/sso: invalid config")
	// ErrInvalidToken — токен не разобран (не JWS, чужой alg, битая структура).
	ErrInvalidToken = errors.New("hubkit/sso: malformed session token")
	// ErrUnknownKey — kid токена отсутствует в JWKS контура.
	ErrUnknownKey = errors.New("hubkit/sso: no signing key for kid")
	// ErrJWKSUnavailable — JWKS контура недоступен: верификатор не может судить
	// о подписи. Extractor переводит её в httpkit.ErrAuthUnavailable (503).
	ErrJWKSUnavailable = errors.New("hubkit/sso: jwks unavailable")
	// ErrInvalidClaims — claims не прошли политику контура (iss/aud/exp/nbf/sub/company_id).
	ErrInvalidClaims = errors.New("hubkit/sso: session token claims rejected")
	// ErrNoSubject — в токене нет sub.
	ErrNoSubject = fmt.Errorf("%w: no subject", ErrInvalidClaims)
	// ErrNoExpiry — в токене нет exp: бессрочная сессия не принимается.
	ErrNoExpiry = fmt.Errorf("%w: no expiry", ErrInvalidClaims)
	// ErrNoCompany — customer-токен без company_id: видимость заказчика
	// определяется его компанией, пустое значение читалось бы как «любая».
	ErrNoCompany = fmt.Errorf("%w: customer session without company_id", ErrInvalidClaims)
)

// Claim* — имена нестандартных claim'ов hub-session JWT (контракт между
// hubkit/issuer и hubkit/sso, определён в hubkit/hubclaims).
// sub/exp/iat/iss/aud — стандартные RFC 7519.
const (
	ClaimEmployeeID  = hubclaims.EmployeeID
	ClaimUsername    = hubclaims.Username
	ClaimName        = hubclaims.Name
	ClaimEmail       = hubclaims.Email
	ClaimRole        = hubclaims.Role
	ClaimSID         = hubclaims.SID
	ClaimKind        = hubclaims.Kind
	ClaimCompanyID   = hubclaims.CompanyID
	ClaimAccountRole = hubclaims.AccountRole
	ClaimProducts    = hubclaims.Products
)

// allowedAlgs — только ES256 (асимметрия): продукт держит лишь публичный ключ,
// симметричную подпись мог бы подделать любой держатель секрета.
var allowedAlgs = []jose.SignatureAlgorithm{jose.ES256}

const defaultHTTPTimeout = 10 * time.Second

// Doer — минимальный HTTP-клиент для загрузки JWKS. Ему удовлетворяет и
// *httpclient.Client (retry + breaker + OTel), и *http.Client.
type Doer = jwks.Doer

// Option — настройка верификатора, передаётся в NewVerifier/NewCustomerVerifier.
type Option func(*options)

type options struct {
	clock clockkit.Clock
}

// WithClock подменяет часы проверки сроков токена (детерминированные тесты).
func WithClock(clock clockkit.Clock) Option {
	return func(o *options) {
		if clock != nil {
			o.clock = clock
		}
	}
}

// Verifier проверяет hub-session JWT по JWKS своего контура. Ключи грузятся
// лениво (первый Verify или неизвестный kid), кэшируются в памяти.
type Verifier struct {
	issuer   string
	audience string
	kind     string
	leeway   time.Duration
	clock    clockkit.Clock
	keys     *jwks.Cache
	instr    *instruments
}

// NewVerifier строит Verifier сотруднического контура из Config. JWKS не
// грузится в конструкторе: discovery ключей не должен блокировать старт
// приложения и переживает временную недоступность хаба (ключи подтянутся на
// первом Verify). nil doer → http.Client с таймаутом 10s.
func NewVerifier(cfg Config, doer Doer, opts ...Option) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return buildVerifier(cfg, cfg.JWKSURL, cfg.Issuer, cfg.Audience, KindEmployee, doer, opts), nil
}

func buildVerifier(cfg Config, jwksURL, issuer, audience, kind string, doer Doer, opts []Option) *Verifier {
	o := options{clock: clockkit.Real{}}
	for _, apply := range opts {
		apply(&o)
	}
	v := &Verifier{
		issuer:   issuer,
		audience: audience,
		kind:     kind,
		leeway:   cfg.Leeway,
		clock:    o.clock,
		instr:    newInstruments(kind),
	}
	v.keys = jwks.New(
		jwksURL, doer,
		jwks.WithTimeout(defaultHTTPTimeout),
		jwks.WithClock(o.clock),
		// Ротация ключа issuer'а не должна отзывать уже выданные access-токены:
		// выведенный из набора kid проверяет подписи ещё KeyGrace.
		jwks.WithRetainPrevious(cfg.KeyGrace),
		// Период перечитывания набора — из Cache-Control ответа JWKS
		// (HUB_SESSION_JWKS_MAX_AGE issuer'а): без него снятый ключ, чей kid не
		// менялся, оставался бы доверенным до перезапуска продукта.
		jwks.WithRespectCacheControl(),
		jwks.WithOnRefresh(v.instr.refreshed),
	)
	return v
}

// CustomerVerifier — верификатор контура заказчика (JWKS company-backend,
// kind=customer). Отдельный тип, чтобы два верификатора одного контракта не
// путались в DI и в сигнатурах.
type CustomerVerifier struct{ *Verifier }

// NewCustomerVerifier строит верификатор сессии заказчика. Пустой
// CustomerJWKSURL → (nil, nil): контур выключен, продукт живёт как без него.
func NewCustomerVerifier(cfg Config, doer Doer, opts ...Option) (*CustomerVerifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.CustomerEnabled() {
		return nil, nil //nolint:nilnil // пустой JWKS-URL = контур выключен, это не ошибка
	}
	v := buildVerifier(cfg, cfg.CustomerJWKSURL, cfg.CustomerIssuer, cfg.CustomerAudience, KindCustomer, doer, opts)
	return &CustomerVerifier{Verifier: v}, nil
}

// Kind — контур верификатора: KindEmployee | KindCustomer.
func (v *Verifier) Kind() string { return v.kind }

// Refresh загружает набор ключей контура без троттлинга (прогрев на старте,
// health-проба потребителя).
func (v *Verifier) Refresh(ctx context.Context) error {
	if err := v.keys.Refresh(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrJWKSUnavailable, err)
	}
	return nil
}

// Verify проверяет подпись/claims токена и возвращает Identity; Kind всегда
// заполнен (пустой claim читается как employee).
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	id, result, err := v.verify(ctx, raw)
	v.instr.verified(ctx, result)
	return id, err
}

// hubClaims — нестандартная часть контракта hub-session JWT.
type hubClaims struct {
	EmployeeID  string   `json:"employee_id"`
	Username    string   `json:"username"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	SID         string   `json:"sid"`
	Kind        string   `json:"kind"`
	CompanyID   string   `json:"company_id"`
	AccountRole string   `json:"account_role"`
	Products    []string `json:"products"`
}

func (v *Verifier) verify(ctx context.Context, raw string) (Identity, string, error) {
	tok, err := jwt.ParseSigned(raw, allowedAlgs)
	if err != nil {
		return Identity{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if len(tok.Headers) == 0 {
		return Identity{}, "malformed", ErrInvalidToken
	}
	key, err := v.keys.Key(ctx, tok.Headers[0].KeyID)
	if err != nil {
		if errors.Is(err, jwks.ErrUnknownKey) {
			return Identity{}, "unknown_key", ErrUnknownKey
		}
		return Identity{}, "jwks_error", fmt.Errorf("%w: %w", ErrJWKSUnavailable, err)
	}

	var claims jwt.Claims
	var custom hubClaims
	if err := tok.Claims(key.Key, &claims, &custom); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return Identity{}, "signature_invalid", fmt.Errorf("hubkit/sso: verify signature: %w", err)
		}
		return Identity{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if err := v.validateClaims(claims, custom); err != nil {
		if errors.Is(err, ErrWrongKind) {
			return Identity{}, "wrong_kind", err
		}
		return Identity{}, "invalid_claims", err
	}

	kind := custom.Kind
	if kind == "" {
		kind = KindEmployee
	}
	return Identity{
		EmployeeID:  custom.EmployeeID,
		Subject:     claims.Subject,
		Username:    custom.Username,
		Name:        custom.Name,
		Email:       custom.Email,
		Role:        custom.Role,
		SID:         custom.SID,
		Kind:        kind,
		CompanyID:   custom.CompanyID,
		AccountRole: custom.AccountRole,
		Products:    custom.Products,
	}, "ok", nil
}

func (v *Verifier) validateClaims(claims jwt.Claims, custom hubClaims) error {
	// Токен без exp — бессрочная сессия: отзывать её нечем, поэтому не принимаем.
	if claims.Expiry == nil {
		return ErrNoExpiry
	}
	expected := jwt.Expected{
		Time:        v.clock.Now(),
		AnyAudience: jwt.Audience{v.audience},
		Issuer:      v.issuer,
	}
	if err := claims.ValidateWithLeeway(expected, v.leeway); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidClaims, err)
	}
	if claims.Subject == "" {
		return ErrNoSubject
	}
	// Контур — до всего остального: подпись и iss могут сойтись (общий ключ в
	// dev), но токен другого контура в этом слоте значит подмену принципала.
	kind := custom.Kind
	if kind == "" {
		kind = KindEmployee
	}
	if kind != v.kind {
		return fmt.Errorf("%w: %s", ErrWrongKind, kind)
	}
	if kind == KindCustomer && custom.CompanyID == "" {
		return ErrNoCompany
	}
	return nil
}
