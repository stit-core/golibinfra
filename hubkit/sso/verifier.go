// Package sso — верификация hub-сессии в продуктах экосистемы stit.
//
// Два контура identity, каждый со своим issuer/ключом/cookie: сотрудник
// (stit_session, employee-backend id.stit.tech) и заказчик (stit_customer,
// company-backend api.hub.stit.tech). Extractor читает оба слота с приоритетом
// сотрудника; верификатор слота проверяет ES256-подпись по JWKS своего контура
// (кэш в памяти, refresh по неизвестному kid не чаще раза в минуту), iss/aud/exp
// и claim kind, после чего кладёт Identity в ctx через httpkit.AuthExtractor.
// Контур заказчика включается заданием HUB_SSO_CUSTOMER_JWKS_URL. Эмитит
// hub_sso_verifications_total{contour,result} и hub_sso_jwks_refresh_total{contour,result}.
//
// Пара к этому пакету — hubkit/issuer (выпускает те же токены); контракт claim'ов
// (employee_id/username/name/email/role/sid + kind/company_id/account_role/
// products) фиксируется здесь константами Claim*.
package sso

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

var (
	ErrConfig           = errors.New("hubkit/sso: HUB_SSO_JWKS_URL required")
	ErrAudience         = errors.New("hubkit/sso: HUB_SSO_AUDIENCE required")
	ErrCustomerIssuer   = errors.New("hubkit/sso: HUB_SSO_CUSTOMER_ISSUER required")
	ErrCustomerAudience = errors.New("hubkit/sso: HUB_SSO_CUSTOMER_AUDIENCE required")
	ErrInvalidToken     = errors.New("hubkit/sso: malformed session token")
	ErrUnknownKey       = errors.New("hubkit/sso: no signing key for kid")
	ErrNoSubject        = errors.New("hubkit/sso: session token has no subject")
)

// Claim* — имена нестандартных claim'ов hub-session JWT (часть контракта между
// hubkit/issuer и hubkit/sso). sub/exp/iat/iss/aud — стандартные RFC 7519.
// Claim{Kind,CompanyID,AccountRole,Products} — контур заказчика.
const (
	ClaimEmployeeID  = "employee_id"
	ClaimUsername    = "username"
	ClaimName        = "name"
	ClaimEmail       = "email"
	ClaimRole        = "role"
	ClaimSID         = "sid"
	ClaimKind        = "kind"
	ClaimCompanyID   = "company_id"
	ClaimAccountRole = "account_role"
	ClaimProducts    = "products"
)

// allowedAlgs — только ES256 (асимметрия): продукт держит лишь публичный ключ,
// симметричную подпись мог бы подделать любой держатель секрета.
var allowedAlgs = []jose.SignatureAlgorithm{jose.ES256}

const defaultHTTPTimeout = 10 * time.Second

// Doer — минимальный HTTP-клиент для загрузки JWKS. Ему удовлетворяет и
// *httpclient.Client (retry + breaker + OTel), и *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Verifier проверяет hub-session JWT по JWKS своего контура. Ключи грузятся
// лениво (первый Verify или неизвестный kid), кэшируются в памяти. Контур
// (kind) — часть проверки: токен чужого контура отвергается, даже если подпись
// и iss сошлись.
type Verifier struct {
	issuer   string
	audience string
	kind     string
	keys     *jwks.Cache
	instr    *instruments
}

// NewVerifier строит Verifier сотруднического контура из Config. JWKS не
// грузится в конструкторе: discovery ключей не должен блокировать старт
// приложения и переживает временную недоступность хаба (ключи подтянутся на
// первом Verify). nil doer → http.Client с таймаутом 10s.
func NewVerifier(cfg Config, doer Doer) (*Verifier, error) {
	if cfg.JWKSURL == "" {
		return nil, ErrConfig
	}
	if cfg.Audience == "" {
		return nil, ErrAudience
	}
	return buildVerifier(cfg.JWKSURL, cfg.Issuer, cfg.Audience, KindEmployee, doer), nil
}

func buildVerifier(jwksURL, issuer, audience, kind string, doer Doer) *Verifier {
	v := &Verifier{issuer: issuer, audience: audience, kind: kind, instr: newInstruments(kind)}
	v.keys = jwks.New(jwksURL, doer, jwks.Options{Timeout: defaultHTTPTimeout, OnRefresh: v.instr.refreshed})
	return v
}

// CustomerVerifier — верификатор контура заказчика (JWKS company-backend,
// kind=customer). Отдельный тип, чтобы два верификатора одного контракта не
// путались в DI и в сигнатурах.
type CustomerVerifier struct{ *Verifier }

// NewCustomerVerifier строит верификатор сессии заказчика. Пустой
// CustomerJWKSURL → (nil, nil): контур выключен, продукт живёт как без него.
// iss и aud обязательны: вместе с kind они не дают токену чужого контура пройти
// в customer-слот.
func NewCustomerVerifier(cfg Config, doer Doer) (*CustomerVerifier, error) {
	if !cfg.CustomerEnabled() {
		return nil, nil //nolint:nilnil // пустой JWKS-URL = контур выключен, это не ошибка
	}
	if cfg.CustomerIssuer == "" {
		return nil, ErrCustomerIssuer
	}
	if cfg.CustomerAudience == "" {
		return nil, ErrCustomerAudience
	}
	return &CustomerVerifier{Verifier: buildVerifier(cfg.CustomerJWKSURL, cfg.CustomerIssuer, cfg.CustomerAudience, KindCustomer, doer)}, nil
}

// Verify проверяет подпись/claims токена и возвращает Identity; Kind всегда
// заполнен (пустой claim читается как employee).
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	id, result, err := v.verify(ctx, raw)
	v.instr.verified(ctx, result)
	return id, err
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
		return Identity{}, "jwks_error", fmt.Errorf("hubkit/sso: jwks: %w", err)
	}

	var claims jwt.Claims
	var custom struct {
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
	if err := tok.Claims(key.Key, &claims, &custom); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return Identity{}, "signature_invalid", fmt.Errorf("hubkit/sso: verify signature: %w", err)
		}
		return Identity{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	expected := jwt.Expected{Time: time.Now(), AnyAudience: jwt.Audience{v.audience}}
	if v.issuer != "" {
		expected.Issuer = v.issuer
	}
	if err := claims.Validate(expected); err != nil {
		return Identity{}, "invalid_claims", fmt.Errorf("hubkit/sso: claims: %w", err)
	}
	if claims.Subject == "" {
		return Identity{}, "invalid_claims", ErrNoSubject
	}
	// Контур — до всего остального: подпись и iss могут сойтись (общий ключ в
	// dev, незаданный HUB_SSO_ISSUER), но токен другого контура в этом слоте
	// значит подмену принципала, а не «почти то же самое».
	kind := custom.Kind
	if kind == "" {
		kind = KindEmployee
	}
	if kind != v.kind {
		return Identity{}, "wrong_kind", fmt.Errorf("%w: %s", ErrWrongKind, kind)
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
