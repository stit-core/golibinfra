// Package oidckit — OIDC BFF-контур: relying-party (discovery, JWKS-кэш,
// verify ID-token, Authorization Code + PKCE), huma-роуты login/callback/me/logout,
// cookie-сессия HS256 (authnkit) и extractor для httpkit.AuthExtractor.
package oidckit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

var (
	// ErrRPConfig — не задан issuer или client id relying-party.
	ErrRPConfig = errors.New("oidckit: issuer and client id required")
	// ErrIssuerMismatch — issuer в discovery-документе не совпал с настроенным.
	ErrIssuerMismatch = errors.New("oidckit: discovery issuer mismatch")
	// ErrNoJWKS — в discovery-документе нет jwks_uri.
	ErrNoJWKS = errors.New("oidckit: discovery has no jwks_uri")
	// ErrNoEndpoints — в discovery-документе нет пригодного authorization/token endpoint'а.
	ErrNoEndpoints = errors.New("oidckit: discovery has no authorization/token endpoint")
	// ErrInvalidToken — ID-token не разобран или подписан неприемлемым алгоритмом.
	ErrInvalidToken = errors.New("oidckit: malformed id token")
	// ErrUnknownKey — в JWKS нет ключа с kid из заголовка токена.
	ErrUnknownKey = errors.New("oidckit: no signing key for kid")
	// ErrNoSubject — в ID-token'е нет claim'а sub.
	ErrNoSubject = errors.New("oidckit: id token has no subject")
	// ErrNoExpiry — в ID-token'е нет claim'а exp (OIDC Core §2 — REQUIRED).
	ErrNoExpiry = errors.New("oidckit: id token has no expiry")
	// ErrAudience — aud/azp не указывают на этого клиента.
	ErrAudience = errors.New("oidckit: id token audience mismatch")
	// ErrNonceMismatch — nonce токена не совпал с сохранённым в state.
	ErrNonceMismatch = errors.New("oidckit: id token nonce mismatch")
	// ErrNoIDToken — ответ token endpoint'а без id_token.
	ErrNoIDToken = errors.New("oidckit: token response has no id_token")
	// ErrExchange — token endpoint ответил не-200.
	ErrExchange = errors.New("oidckit: code exchange failed")
)

// allowedAlgs — асимметричные подписи IdP (симметрию не принимаем: RP не делит
// секрет подписи с IdP, HS-токен мог бы подписать кто угодно со client secret'ом).
var allowedAlgs = []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.ES256, jose.ES384}

const (
	oidcScope          = "openid profile email"
	defaultHTTPTimeout = 10 * time.Second
	maxDiscoveryBody   = 1 << 20

	// keyGrace — выведенный из JWKS ключ проверяет подписи ещё столько после
	// ротации: токен, выпущенный за миг до неё, не должен отвергаться.
	keyGrace = 10 * time.Minute
)

// Doer — минимальный HTTP-клиент похода в IdP; ему удовлетворяют
// *httpclient.Client (retry + breaker + OTel-спаны) и *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// RPConfig — параметры relying-party для Verifier (программные; env-контур
// BFF читает Config и строит RPConfig сам).
type RPConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Leeway — допуск часов при проверке exp/nbf/iat; 0 → DefaultLeeway.
	Leeway time.Duration
	// AllowInsecure разрешает issuer и endpoint'ы по http (dev-стенд).
	AllowInsecure bool
	// Clock — часы проверки claim'ов; nil → clockkit.Real.
	Clock clockkit.Clock
}

// Claims — то, что вытащили из проверенного ID-token'а.
type Claims struct {
	Subject  string
	Username string // preferred_username — логин (напр. j.doe)
	Name     string // name — отображаемое имя (напр. Jane Doe)
	Email    string
	// Groups — claim `groups` IdP, если он есть: основа маппинга «группа → роль».
	Groups []string
	// Raw — все claims проверенного токена: то, чего нет в типизированных полях.
	Raw map[string]any
}

// Verifier — OIDC relying-party: discovery /.well-known, JWKS-кэш (refresh по
// неизвестному kid не чаще раза в минуту, singleflight), verify iss/aud/azp/exp/nonce,
// обмен authorization code.
type Verifier struct {
	issuer        string
	clientID      string
	clientSecret  string
	redirectURL   string
	authEndpoint  string
	tokenEndpoint string
	jwksURI       string
	leeway        time.Duration
	clock         clockkit.Clock
	http          Doer
	keys          *jwks.Cache
	instr         *instruments
}

// NewVerifier делает discovery и первичную загрузку JWKS; nil doer →
// http.Client с таймаутом 10s.
func NewVerifier(ctx context.Context, cfg RPConfig, doer Doer) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, ErrRPConfig
	}
	if doer == nil {
		doer = &http.Client{Timeout: defaultHTTPTimeout}
	}
	leeway := cfg.Leeway
	if leeway <= 0 {
		leeway = DefaultLeeway
	}
	clock := cfg.Clock
	if clock == nil {
		clock = clockkit.Real{}
	}
	v := &Verifier{
		issuer:       strings.TrimRight(cfg.Issuer, "/"),
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		leeway:       leeway,
		clock:        clock,
		http:         doer,
		instr:        newInstruments(),
	}

	start := time.Now()
	err := v.bootstrap(ctx, cfg.AllowInsecure)
	v.instr.discovered(ctx, time.Since(start), err)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Verifier) bootstrap(ctx context.Context, allowInsecure bool) error {
	if err := v.discover(ctx, allowInsecure); err != nil {
		return err
	}
	v.keys = jwks.New(v.jwksURI, v.http,
		jwks.WithTimeout(defaultHTTPTimeout),
		jwks.WithRetainPrevious(keyGrace),
		jwks.WithOnRefresh(v.instr.refreshed),
	)
	if err := v.keys.Refresh(ctx); err != nil {
		return fmt.Errorf("oidckit: jwks: %w", err)
	}
	return nil
}

// KeysLoaded — загружен ли набор ключей проверки подписи (health-проба).
func (v *Verifier) KeysLoaded() bool { return v.keys.Loaded() }

// AuthCodeURL строит redirect на authorization_endpoint (Authorization Code + PKCE S256).
func (v *Verifier) AuthCodeURL(state, nonce, challenge string) string {
	u, err := url.Parse(v.authEndpoint)
	if err != nil {
		return v.authEndpoint
	}
	q := u.Query()
	q.Set("client_id", v.clientID)
	q.Set("redirect_uri", v.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", oidcScope)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String()
}

// Exchange меняет authorization code на ID-token (token_endpoint), проверяет его и nonce.
func (v *Verifier) Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Claims, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", v.redirectURL)
	form.Set("client_id", v.clientID)
	form.Set("client_secret", v.clientSecret)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := v.http.Do(req)
	if err != nil {
		return Claims{}, fmt.Errorf("oidckit: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Claims{}, fmt.Errorf("%w: status %d", ErrExchange, resp.StatusCode)
	}
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryBody)).Decode(&tr); err != nil {
		return Claims{}, fmt.Errorf("oidckit: decode token response: %w", err)
	}
	if tr.IDToken == "" {
		return Claims{}, ErrNoIDToken
	}
	return v.verifyToken(ctx, tr.IDToken, expectedNonce)
}

// Verify проверяет ID-token без сверки nonce (для сценариев без code-flow).
func (v *Verifier) Verify(ctx context.Context, rawIDToken string) (Claims, error) {
	return v.verifyToken(ctx, rawIDToken, "")
}

func (v *Verifier) verifyToken(ctx context.Context, rawIDToken, expectedNonce string) (Claims, error) {
	c, result, err := v.verify(ctx, rawIDToken, expectedNonce)
	v.instr.verified(ctx, result)
	return c, err
}

func (v *Verifier) verify(ctx context.Context, rawIDToken, expectedNonce string) (Claims, string, error) {
	tok, err := jwt.ParseSigned(rawIDToken, allowedAlgs)
	if err != nil {
		return Claims{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if len(tok.Headers) == 0 {
		return Claims{}, "malformed", ErrInvalidToken
	}
	key, err := v.keys.Key(ctx, tok.Headers[0].KeyID)
	if err != nil {
		if errors.Is(err, jwks.ErrUnknownKey) {
			return Claims{}, "unknown_key", ErrUnknownKey
		}
		return Claims{}, "jwks_error", fmt.Errorf("oidckit: jwks: %w", err)
	}

	var claims jwt.Claims
	var custom struct {
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Nonce             string `json:"nonce"`
		AZP               string `json:"azp"`
	}
	raw := map[string]any{}
	if err := tok.Claims(key.Key, &claims, &custom, &raw); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return Claims{}, "signature_invalid", fmt.Errorf("oidckit: verify signature: %w", err)
		}
		return Claims{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.clientID},
		Time:        v.clock.Now(),
	}, v.leeway); err != nil {
		return Claims{}, "invalid_claims", fmt.Errorf("oidckit: claims: %w", err)
	}
	if claims.Expiry == nil {
		return Claims{}, "invalid_claims", ErrNoExpiry
	}
	if claims.Subject == "" {
		return Claims{}, "invalid_claims", ErrNoSubject
	}
	// OIDC Core §3.1.3.7: при нескольких aud обязателен azp, а заданный azp
	// обязан совпасть с client_id — иначе это токен другого клиента того же IdP.
	if (len(claims.Audience) > 1 && custom.AZP == "") || (custom.AZP != "" && custom.AZP != v.clientID) {
		return Claims{}, "invalid_claims", ErrAudience
	}
	if expectedNonce != "" && custom.Nonce != expectedNonce {
		return Claims{}, "nonce_mismatch", ErrNonceMismatch
	}
	return Claims{
		Subject:  claims.Subject,
		Username: custom.PreferredUsername,
		Name:     custom.Name,
		Email:    custom.Email,
		Groups:   groupsFrom(raw),
		Raw:      raw,
	}, "ok", nil
}

func groupsFrom(raw map[string]any) []string {
	switch v := raw["groups"].(type) {
	case []any:
		groups := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				groups = append(groups, s)
			}
		}
		return groups
	case string:
		return []string{v}
	default:
		return nil
	}
}

func (v *Verifier) discover(ctx context.Context, allowInsecure bool) error {
	endpoint := v.issuer + "/.well-known/openid-configuration"
	var doc struct {
		Issuer                string `json:"issuer"`
		JWKSURI               string `json:"jwks_uri"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := v.getJSON(ctx, endpoint, &doc); err != nil {
		return fmt.Errorf("oidckit: discovery: %w", err)
	}
	if strings.TrimRight(doc.Issuer, "/") != v.issuer {
		return ErrIssuerMismatch
	}
	if doc.JWKSURI == "" || !absoluteURL(doc.JWKSURI, allowInsecure) {
		return ErrNoJWKS
	}
	if !absoluteURL(doc.AuthorizationEndpoint, allowInsecure) || !absoluteURL(doc.TokenEndpoint, allowInsecure) {
		return ErrNoEndpoints
	}
	// iss в токенах сверяется с тем, что опубликовал сам IdP.
	v.issuer = doc.Issuer
	v.jwksURI = doc.JWKSURI
	v.authEndpoint = doc.AuthorizationEndpoint
	v.tokenEndpoint = doc.TokenEndpoint
	return nil
}

func absoluteURL(raw string, allowInsecure bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (allowInsecure && u.Scheme == "http")
}

func (v *Verifier) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidckit: GET %s: status %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryBody)).Decode(dst)
}
