package oidckit

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

const (
	stateKeyPrefix = "oidc:login:"
	stateTTL       = 10 * time.Minute

	defaultRoutePrefix = "/auth"
)

// Config — env-конфиг BFF-контура (имена OIDC_* / COOKIE_SECURE — public API kit'а).
type Config struct {
	Issuer       string `envconfig:"OIDC_ISSUER"`
	ClientID     string `envconfig:"OIDC_CLIENT_ID"`
	ClientSecret string `envconfig:"OIDC_CLIENT_SECRET"`
	RedirectURL  string `envconfig:"OIDC_REDIRECT_URL"`
	PostLoginURL string `default:"/"                    envconfig:"OIDC_POST_LOGIN_URL"`
	// SessionTTL — срок жизни cookie-сессии (exp session-JWT).
	SessionTTL time.Duration `default:"12h" envconfig:"OIDC_SESSION_TTL"`
	// RoutePrefix — префикс BFF-роутов (login/callback/me/logout); программный,
	// не env: пути — часть API-контракта сервиса, пусто → "/auth".
	RoutePrefix string `ignored:"true"`
}

func (c Config) enabled() bool { return c.Issuer != "" && c.ClientID != "" }

func (c Config) routePrefix() string {
	p := strings.TrimRight(c.RoutePrefix, "/")
	if p == "" {
		return defaultRoutePrefix
	}
	return p
}

// Hooks — точки расширения BFF; оба поля опциональны.
type Hooks struct {
	// SessionClaims маппит проверенные claims ID-token'а в дополнительные claims
	// session-JWT (например "role" по списку админов). Ключи sub /
	// preferred_username / name / email заполняет сам kit; hook может их переопределить.
	SessionClaims func(c Claims) map[string]any
	// OnLogin выполняется после успешного exchange+verify, до выдачи session-cookie
	// (JIT-провижининг пользователя у консьюмера). Ошибка отменяет вход.
	OnLogin func(ctx context.Context, c Claims) error
}

type loginState struct {
	Nonce        string `json:"nonce"`
	CodeVerifier string `json:"code_verifier"`
	ReturnTo     string `json:"return_to,omitempty"`
}

// sanitizeReturnTo пропускает только внутренний absolute-path ("/join/x") —
// внешние URL и protocol-relative ("//evil") отбрасываются (open-redirect).
func sanitizeReturnTo(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}
	return raw
}

// BFF ведёт Authorization-Code-flow: login → idp → callback, после чего минтит
// session-cookie (HS256/JWT_SECRET), которую читает Extractor; плюс me/logout.
type BFF struct {
	cfg    Config
	client *http.Client
	signer authnkit.JWTSigner
	store  StateStore
	cookie CookieConfig
	hooks  Hooks
	log    logkit.Logger

	mu sync.Mutex
	v  *Verifier
}

func NewBFF(cfg Config, signer authnkit.JWTSigner, store StateStore, cookie CookieConfig, hooks Hooks, log logkit.Logger) *BFF {
	return &BFF{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		signer: signer,
		store:  store,
		cookie: cookie,
		hooks:  hooks,
		log:    log.With("component", "oidc"),
	}
}

// verifier лениво инициализируется: discovery к idp не должно блокировать старт приложения.
func (b *BFF) verifier(ctx context.Context) (*Verifier, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.v != nil {
		return b.v, nil
	}
	v, err := NewVerifier(ctx, RPConfig{
		Issuer:       b.cfg.Issuer,
		ClientID:     b.cfg.ClientID,
		ClientSecret: b.cfg.ClientSecret,
		RedirectURL:  b.cfg.RedirectURL,
	}, b.client)
	if err != nil {
		return nil, err
	}
	b.v = v
	return v, nil
}

type redirectOutput struct {
	Location  string        `header:"Location"`
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

type callbackInput struct {
	Code  string `query:"code"`
	State string `query:"state"`
}

type loginInput struct {
	ReturnTo string `query:"return_to"`
}

func (b *BFF) login(ctx context.Context, in *loginInput) (*redirectOutput, error) {
	if !b.cfg.enabled() {
		return nil, huma.Error503ServiceUnavailable("oidc not configured")
	}
	v, err := b.verifier(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("oidc discovery failed", err)
	}

	state, err := RandomToken()
	if err != nil {
		return nil, err
	}
	nonce, err := RandomToken()
	if err != nil {
		return nil, err
	}
	codeVerifier, challenge, err := PKCE()
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(loginState{
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		ReturnTo:     sanitizeReturnTo(in.ReturnTo),
	})
	if err != nil {
		return nil, err
	}
	if err := b.store.Set(ctx, stateKeyPrefix+state, raw, stateTTL); err != nil {
		return nil, huma.Error502BadGateway("state store", err)
	}

	return &redirectOutput{Location: v.AuthCodeURL(state, nonce, challenge)}, nil
}

func (b *BFF) callback(ctx context.Context, in *callbackInput) (*redirectOutput, error) {
	if !b.cfg.enabled() {
		return nil, huma.Error503ServiceUnavailable("oidc not configured")
	}
	if in.Code == "" || in.State == "" {
		return nil, huma.Error400BadRequest("missing code or state")
	}

	raw, ok, err := b.store.GetDel(ctx, stateKeyPrefix+in.State)
	if err != nil {
		return nil, huma.Error502BadGateway("state store", err)
	}
	if !ok {
		return nil, huma.Error400BadRequest("unknown or expired state")
	}

	var st loginState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, huma.Error400BadRequest("corrupt state")
	}

	v, err := b.verifier(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("oidc discovery failed", err)
	}
	claims, err := v.Exchange(ctx, in.Code, st.CodeVerifier, st.Nonce)
	if err != nil {
		b.log.Warn(ctx, "oidc exchange failed", "error", err)
		return nil, huma.Error401Unauthorized("oidc exchange failed")
	}

	if b.hooks.OnLogin != nil {
		if err := b.hooks.OnLogin(ctx, claims); err != nil {
			b.log.Warn(ctx, "oidc login hook failed", "error", err)
			return nil, huma.Error500InternalServerError("login hook failed")
		}
	}

	session, err := b.signer.Sign(b.sessionClaims(claims), b.cfg.SessionTTL)
	if err != nil {
		return nil, huma.Error500InternalServerError("session sign", err)
	}

	return &redirectOutput{
		Location:  b.postLoginURL(st.ReturnTo),
		SetCookie: []http.Cookie{SessionCookie(session, b.cookie)},
	}, nil
}

func (b *BFF) sessionClaims(c Claims) map[string]any {
	claims := map[string]any{
		"sub":                c.Subject,
		"preferred_username": c.Username,
		"name":               c.Name,
		"email":              c.Email,
	}
	if b.hooks.SessionClaims != nil {
		for k, v := range b.hooks.SessionClaims(c) {
			claims[k] = v
		}
	}
	return claims
}

func (b *BFF) postLoginURL(returnTo string) string {
	if returnTo != "" {
		return returnTo
	}
	if b.cfg.PostLoginURL == "" {
		return "/"
	}
	return b.cfg.PostLoginURL
}

// MeView — публичная проекция сессионной identity для фронта.
type MeView struct {
	Subject  string `json:"sub"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role,omitempty"`
}

type meOutput struct {
	Body struct {
		Data MeView `json:"data"`
	}
}

func (b *BFF) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	id, ok := IdentityFrom(ctx)
	if !ok || id.Subject == "" {
		return nil, huma.Error401Unauthorized("no sso session")
	}
	out := &meOutput{}
	out.Body.Data = MeView(id)
	return out, nil
}

type logoutOutput struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

func (b *BFF) logout(_ context.Context, _ *struct{}) (*logoutOutput, error) {
	return &logoutOutput{SetCookie: ExpiredSessionCookies(b.cookie)}, nil
}

// RegisterRoutes регистрирует BFF-роуты под Config.RoutePrefix (default /auth):
// GET <p>/login, GET <p>/callback, GET <p>/me (Security: Cookie), POST <p>/logout.
func RegisterRoutes(api huma.API, b *BFF) {
	p := b.cfg.routePrefix()

	huma.Register(api, huma.Operation{
		OperationID:   "oidcLogin",
		Method:        http.MethodGet,
		Path:          p + "/login",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusFound,
		Summary:       "Начать OIDC-вход (redirect на idp)",
	}, b.login)

	huma.Register(api, huma.Operation{
		OperationID:   "oidcCallback",
		Method:        http.MethodGet,
		Path:          p + "/callback",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusFound,
		Summary:       "OIDC callback: обмен кода на session-cookie",
	}, b.callback)

	huma.Register(api, huma.Operation{
		OperationID: "authMe",
		Method:      http.MethodGet,
		Path:        p + "/me",
		Tags:        []string{"auth"},
		Security:    []map[string][]string{{SecuritySchemeName: {}}},
		Summary:     "Текущая identity из session-cookie",
	}, b.me)

	huma.Register(api, huma.Operation{
		OperationID:   "authLogout",
		Method:        http.MethodPost,
		Path:          p + "/logout",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Summary:       "Выход: сброс session-cookie",
	}, b.logout)
}
