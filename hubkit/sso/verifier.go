// Package sso — верификация hub-сессии в продуктах экосистемы stit.
//
// Читает cookie stit_session, проверяет ES256-подпись hub-session JWT по JWKS
// employee-backend (id.stit.tech) с in-memory кэшем ключей и lazy-refresh по
// неизвестному kid (тот же приём, что в oidckit/verifier.go), сверяет
// iss/aud/exp и кладёт Identity в ctx через httpkit.AuthExtractor. Ни одного
// похода по сети на запрос после первичной загрузки JWKS — продукт живёт при
// недоступном хабе, пока ключи в кэше.
//
// Пара к этому пакету — hubkit/issuer (выпускает те же токены); контракт claim'ов
// (employee_id/username/name/email/role/sid) фиксируется здесь константами Claim*.
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

var (
	ErrConfig       = errors.New("hubkit/sso: HUB_SSO_JWKS_URL required")
	ErrInvalidToken = errors.New("hubkit/sso: malformed session token")
	ErrUnknownKey   = errors.New("hubkit/sso: no signing key for kid")
	ErrNoSubject    = errors.New("hubkit/sso: session token has no subject")
	ErrNoJWKS       = errors.New("hubkit/sso: jwks endpoint returned no keys")
)

// Claim* — имена нестандартных claim'ов hub-session JWT (часть контракта между
// hubkit/issuer и hubkit/sso). sub/exp/iat/iss/aud — стандартные RFC 7519.
const (
	ClaimEmployeeID = "employee_id"
	ClaimUsername   = "username"
	ClaimName       = "name"
	ClaimEmail      = "email"
	ClaimRole       = "role"
	ClaimSID        = "sid"
)

// allowedAlgs — только ES256 (асимметрия): продукт держит лишь публичный ключ,
// симметричную подпись мог бы подделать любой держатель секрета.
var allowedAlgs = []jose.SignatureAlgorithm{jose.ES256}

// Doer — минимальный HTTP-клиент для загрузки JWKS. Ему удовлетворяет и
// *httpclient.Client (retry + breaker + OTel), и *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Verifier проверяет hub-session JWT по JWKS employee-backend. Ключи грузятся
// лениво (первый Verify или неизвестный kid), кэшируются в памяти.
type Verifier struct {
	jwksURL  string
	issuer   string
	audience string
	http     Doer

	mu     sync.RWMutex
	keys   jose.JSONWebKeySet
	loaded bool
}

// NewVerifier строит Verifier из Config. JWKS не грузится в конструкторе:
// discovery ключей не должен блокировать старт приложения и переживает
// временную недоступность хаба (ключи подтянутся на первом Verify).
func NewVerifier(cfg Config, doer Doer) (*Verifier, error) {
	if cfg.JWKSURL == "" {
		return nil, ErrConfig
	}
	if doer == nil {
		doer = &http.Client{Timeout: 10 * time.Second}
	}
	return &Verifier{
		jwksURL:  cfg.JWKSURL,
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		http:     doer,
	}, nil
}

// Verify проверяет подпись/claims токена и возвращает Identity.
func (v *Verifier) Verify(ctx context.Context, raw string) (Identity, error) {
	tok, err := jwt.ParseSigned(raw, allowedAlgs)
	if err != nil {
		return Identity{}, fmt.Errorf("hubkit/sso: parse: %w", err)
	}
	if len(tok.Headers) == 0 {
		return Identity{}, ErrInvalidToken
	}
	key, err := v.keyByID(ctx, tok.Headers[0].KeyID)
	if err != nil {
		return Identity{}, err
	}

	var claims jwt.Claims
	var custom struct {
		EmployeeID string `json:"employee_id"`
		Username   string `json:"username"`
		Name       string `json:"name"`
		Email      string `json:"email"`
		Role       string `json:"role"`
		SID        string `json:"sid"`
	}
	if err := tok.Claims(key, &claims, &custom); err != nil {
		return Identity{}, fmt.Errorf("hubkit/sso: verify signature: %w", err)
	}

	expected := jwt.Expected{Time: time.Now()}
	if v.issuer != "" {
		expected.Issuer = v.issuer
	}
	if v.audience != "" {
		expected.AnyAudience = jwt.Audience{v.audience}
	}
	if err := claims.Validate(expected); err != nil {
		return Identity{}, fmt.Errorf("hubkit/sso: claims: %w", err)
	}
	if claims.Subject == "" {
		return Identity{}, ErrNoSubject
	}

	return Identity{
		EmployeeID: custom.EmployeeID,
		Subject:    claims.Subject,
		Username:   custom.Username,
		Name:       custom.Name,
		Email:      custom.Email,
		Role:       custom.Role,
		SID:        custom.SID,
	}, nil
}

func (v *Verifier) keyByID(ctx context.Context, kid string) (any, error) {
	if k := v.lookup(kid); k != nil {
		return k.Key, nil
	}
	// Промах: либо ключи ещё не грузились, либо kid новый (ротация) — рефрешим.
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	if k := v.lookup(kid); k != nil {
		return k.Key, nil
	}
	return nil, ErrUnknownKey
}

func (v *Verifier) lookup(kid string) *jose.JSONWebKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if !v.loaded {
		return nil
	}
	if kid != "" {
		if ks := v.keys.Key(kid); len(ks) > 0 {
			return &ks[0]
		}
		return nil
	}
	if len(v.keys.Keys) == 1 {
		return &v.keys.Keys[0]
	}
	return nil
}

func (v *Verifier) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return fmt.Errorf("hubkit/sso: jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hubkit/sso: jwks GET %s: status %d", v.jwksURL, resp.StatusCode)
	}
	var ks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&ks); err != nil {
		return fmt.Errorf("hubkit/sso: decode jwks: %w", err)
	}
	if len(ks.Keys) == 0 {
		return ErrNoJWKS
	}
	v.mu.Lock()
	v.keys = ks
	v.loaded = true
	v.mu.Unlock()
	return nil
}
