// Package oidckit — Authentik/OIDC BFF-контур для SSO-входа сотрудника:
// relying-party (discovery, JWKS-кэш, verify ID-token, Authorization Code + PKCE),
// huma-роуты login/callback/me/logout, cookie-сессия HS256 (authnkit) и extractor
// для httpkit.HumaAuth. State логин-флоу — через StateStore (Redis/in-memory).
package oidckit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

var (
	ErrRPConfig       = errors.New("oidckit: issuer and client id required")
	ErrIssuerMismatch = errors.New("oidckit: discovery issuer mismatch")
	ErrNoJWKS         = errors.New("oidckit: discovery has no jwks_uri")
	ErrNoEndpoints    = errors.New("oidckit: discovery has no authorization/token endpoint")
	ErrInvalidToken   = errors.New("oidckit: malformed id token")
	ErrUnknownKey     = errors.New("oidckit: no signing key for kid")
	ErrNoSubject      = errors.New("oidckit: id token has no subject")
	ErrNonceMismatch  = errors.New("oidckit: id token nonce mismatch")
	ErrNoIDToken      = errors.New("oidckit: token response has no id_token")
	ErrExchange       = errors.New("oidckit: code exchange failed")
)

// allowedAlgs — асимметричные подписи IdP (симметрию не принимаем: RP не делит
// секрет подписи с IdP, HS-токен мог бы подписать кто угодно со client secret'ом).
var allowedAlgs = []jose.SignatureAlgorithm{jose.RS256, jose.RS384, jose.RS512, jose.ES256, jose.ES384}

const oidcScope = "openid profile email"

// RPConfig — параметры relying-party для Verifier (программные; env-контур
// BFF читает Config и строит RPConfig сам).
type RPConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Claims — то, что вытащили из проверенного ID-token'а.
type Claims struct {
	Subject  string
	Username string // preferred_username — логин (напр. n.sycheva)
	Name     string // name — ФИО (напр. Наталья Сычева)
	Email    string
}

// Verifier — OIDC relying-party: discovery /.well-known, JWKS-кэш с lazy-refresh
// по неизвестному kid, verify iss/aud/exp/nonce, обмен authorization code.
type Verifier struct {
	issuer        string
	clientID      string
	clientSecret  string
	redirectURL   string
	authEndpoint  string
	tokenEndpoint string
	jwksURI       string
	http          *http.Client

	mu   sync.RWMutex
	keys jose.JSONWebKeySet
}

func NewVerifier(ctx context.Context, cfg RPConfig, client *http.Client) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, ErrRPConfig
	}
	if client == nil {
		client = http.DefaultClient
	}
	v := &Verifier{
		issuer:       cfg.Issuer,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		http:         client,
	}
	if err := v.discover(ctx); err != nil {
		return nil, err
	}
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// AuthCodeURL строит redirect на authorization_endpoint (Authorization Code + PKCE S256).
func (v *Verifier) AuthCodeURL(state, nonce, challenge string) string {
	q := url.Values{}
	q.Set("client_id", v.clientID)
	q.Set("redirect_uri", v.redirectURL)
	q.Set("response_type", "code")
	q.Set("scope", oidcScope)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return v.authEndpoint + "?" + q.Encode()
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
		return Claims{}, ErrExchange
	}
	var tr struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
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
	tok, err := jwt.ParseSigned(rawIDToken, allowedAlgs)
	if err != nil {
		return Claims{}, fmt.Errorf("oidckit: parse: %w", err)
	}
	if len(tok.Headers) == 0 {
		return Claims{}, ErrInvalidToken
	}
	key, err := v.keyByID(ctx, tok.Headers[0].KeyID)
	if err != nil {
		return Claims{}, err
	}

	var claims jwt.Claims
	var custom struct {
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		Nonce             string `json:"nonce"`
	}
	if err := tok.Claims(key, &claims, &custom); err != nil {
		return Claims{}, fmt.Errorf("oidckit: verify signature: %w", err)
	}
	if err := claims.Validate(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.clientID},
		Time:        time.Now(),
	}); err != nil {
		return Claims{}, fmt.Errorf("oidckit: claims: %w", err)
	}
	if claims.Subject == "" {
		return Claims{}, ErrNoSubject
	}
	if expectedNonce != "" && custom.Nonce != expectedNonce {
		return Claims{}, ErrNonceMismatch
	}
	return Claims{
		Subject:  claims.Subject,
		Username: custom.PreferredUsername,
		Name:     custom.Name,
		Email:    custom.Email,
	}, nil
}

func (v *Verifier) discover(ctx context.Context) error {
	endpoint := strings.TrimRight(v.issuer, "/") + "/.well-known/openid-configuration"
	var doc struct {
		Issuer                string `json:"issuer"`
		JWKSURI               string `json:"jwks_uri"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := v.getJSON(ctx, endpoint, &doc); err != nil {
		return fmt.Errorf("oidckit: discovery: %w", err)
	}
	if doc.Issuer != v.issuer {
		return ErrIssuerMismatch
	}
	if doc.JWKSURI == "" {
		return ErrNoJWKS
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return ErrNoEndpoints
	}
	v.jwksURI = doc.JWKSURI
	v.authEndpoint = doc.AuthorizationEndpoint
	v.tokenEndpoint = doc.TokenEndpoint
	return nil
}

func (v *Verifier) refreshKeys(ctx context.Context) error {
	var ks jose.JSONWebKeySet
	if err := v.getJSON(ctx, v.jwksURI, &ks); err != nil {
		return fmt.Errorf("oidckit: jwks: %w", err)
	}
	v.mu.Lock()
	v.keys = ks
	v.mu.Unlock()
	return nil
}

func (v *Verifier) keyByID(ctx context.Context, kid string) (any, error) {
	if k := v.lookup(kid); k != nil {
		return k.Key, nil
	}
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

func (v *Verifier) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidckit: GET %s: status %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
