// Package oidckit — Authentik/OIDC BFF-контур для SSO-входа сотрудника:
// relying-party (discovery, JWKS-кэш, verify ID-token, Authorization Code + PKCE),
// huma-роуты login/callback/me/logout, cookie-сессия HS256 (authnkit) и extractor
// для httpkit.HumaAuth. State логин-флоу — через StateStore поверх cachekit.
// Эмитит oidc_verifications_total{result} и oidc_jwks_refresh_total{result}.
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

	"git.stit.tech/stit-core/golibinfra/internal/jwks"
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

const (
	oidcScope          = "openid profile email"
	defaultHTTPTimeout = 10 * time.Second
	maxDiscoveryBody   = 1 << 20
)

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

// Verifier — OIDC relying-party: discovery /.well-known, JWKS-кэш (refresh по
// неизвестному kid не чаще раза в минуту, singleflight), verify iss/aud/exp/nonce,
// обмен authorization code.
type Verifier struct {
	issuer        string
	clientID      string
	clientSecret  string
	redirectURL   string
	authEndpoint  string
	tokenEndpoint string
	jwksURI       string
	http          *http.Client
	keys          *jwks.Cache
	instr         *instruments
}

// NewVerifier делает discovery и первичную загрузку JWKS; nil client →
// http.Client с таймаутом 10s.
func NewVerifier(ctx context.Context, cfg RPConfig, client *http.Client) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, ErrRPConfig
	}
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	v := &Verifier{
		issuer:       cfg.Issuer,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		redirectURL:  cfg.RedirectURL,
		http:         client,
		instr:        newInstruments(),
	}
	if err := v.discover(ctx); err != nil {
		return nil, err
	}
	v.keys = jwks.New(v.jwksURI, client, jwks.Options{Timeout: defaultHTTPTimeout, OnRefresh: v.instr.refreshed})
	if err := v.keys.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("oidckit: jwks: %w", err)
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
	}
	if err := tok.Claims(key.Key, &claims, &custom); err != nil {
		if errors.Is(err, jose.ErrCryptoFailure) {
			return Claims{}, "signature_invalid", fmt.Errorf("oidckit: verify signature: %w", err)
		}
		return Claims{}, "malformed", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if err := claims.Validate(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.clientID},
		Time:        time.Now(),
	}); err != nil {
		return Claims{}, "invalid_claims", fmt.Errorf("oidckit: claims: %w", err)
	}
	if claims.Subject == "" {
		return Claims{}, "invalid_claims", ErrNoSubject
	}
	if expectedNonce != "" && custom.Nonce != expectedNonce {
		return Claims{}, "nonce_mismatch", ErrNonceMismatch
	}
	return Claims{
		Subject:  claims.Subject,
		Username: custom.PreferredUsername,
		Name:     custom.Name,
		Email:    custom.Email,
	}, "ok", nil
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
