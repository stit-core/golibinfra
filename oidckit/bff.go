package oidckit

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/sync/singleflight"

	"git.stit.tech/stit-core/golibinfra/authnkit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

const (
	stateKeyPrefix = "oidc:login:"
	stateTTL       = 10 * time.Minute

	// discoveryRetryWindow — отрицательный кэш discovery: пока idp лежит,
	// login/callback отвечают ошибкой прошлой попытки, а не встают в очередь
	// на новый поход в сеть.
	discoveryRetryWindow = 5 * time.Second
	// discoveryTimeout — потолок одной попытки discovery+JWKS. Не больше
	// таймаута HTTP-клиента и заведомо меньше fx.DefaultTimeout (15s), иначе
	// старт с OIDC_DISCOVERY_ON_START=true падал бы по таймауту fx, а не по
	// понятной ошибке кита.
	discoveryTimeout = defaultHTTPTimeout

	stageLogin    = "login"
	stageCallback = "callback"
	stageLogout   = "logout"

	noStore = "no-store"
)

// Hooks — точки расширения BFF; оба поля опциональны.
type Hooks struct {
	// SessionClaims маппит проверенные claims ID-token'а в дополнительные claims
	// session-JWT (например "role" по Claims.Groups). Ключи sub /
	// preferred_username / name / email заполняет сам kit; hook может их
	// переопределить, кроме служебного claim'а typ.
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

// sanitizeReturnTo пропускает только внутренний absolute-path ("/join/x").
// Управляющие символы (браузер вырезает TAB/CR/LF до разбора URL), обратный
// слеш (для браузера это "/") и protocol-relative-формы отбрасываются: любая
// из них превращает относительный Location во внешний.
func sanitizeReturnTo(raw string) string {
	if raw == "" || len(raw) > MaxReturnToLen {
		return ""
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || hasUnsafeBytes(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return ""
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || hasUnsafeBytes(u.Path) {
		return ""
	}
	return raw
}

func hasUnsafeBytes(s string) bool {
	for i := range len(s) {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}

// BFF ведёт Authorization-Code-flow: login → idp → callback, после чего минтит
// session-cookie (HS256/JWT_SECRET), которую читает Extractor; плюс me/logout.
// Сессия stateless: logout лишь сбрасывает cookie, выданный JWT живёт до exp.
type BFF struct {
	cfg    Config
	doer   Doer
	signer authnkit.JWTSigner
	store  StateStore
	cookie CookieConfig
	hooks  Hooks
	log    logkit.Logger
	instr  *instruments

	group singleflight.Group
	v     atomic.Pointer[Verifier]

	mu          sync.Mutex
	lastAttempt time.Time
	lastErr     error
}

// NewBFF собирает BFF; невалидный Config/CookieConfig → ErrConfig на старте.
// doer — клиент походов в idp (nil → http.Client с таймаутом 10s).
func NewBFF(
	cfg Config,
	doer Doer,
	signer authnkit.JWTSigner,
	store StateStore,
	cookie CookieConfig,
	hooks Hooks,
	log logkit.Logger,
) (*BFF, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := cookie.Validate(); err != nil {
		return nil, err
	}
	if signer == nil || store == nil {
		return nil, fmt.Errorf("%w: signer and state store are required", ErrConfig)
	}
	if doer == nil {
		doer = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &BFF{
		cfg:    cfg,
		doer:   doer,
		signer: signer,
		store:  store,
		cookie: cookie,
		hooks:  hooks,
		log:    log.With("component", "oidc"),
		instr:  newInstruments(),
	}, nil
}

// Warmup делает discovery и первичную загрузку JWKS заранее (fx-hook при
// OIDC_DISCOVERY_ON_START=true); попытка укладывается в дедлайн ctx, а без
// дедлайна — в discoveryTimeout.
func (b *BFF) Warmup(ctx context.Context) error {
	_, err := b.verifier(ctx)
	return err
}

// Probe — health-проба готовности OIDC: discovery выполнен и ключи проверки
// подписи загружены.
func (b *BFF) Probe() healthkit.Probe {
	return healthkit.New("oidckit", func(ctx context.Context) error {
		v, err := b.verifier(ctx)
		if err != nil {
			return err
		}
		if !v.KeysLoaded() {
			return ErrNoJWKS
		}
		return nil
	})
}

// verifier лениво инициализируется: discovery к idp не должно блокировать старт
// приложения. Параллельные вызовы делят одну попытку (singleflight), неудача
// на discoveryRetryWindow отвечает из отрицательного кэша, а дедлайн ctx
// вызывающего ограничивает его ожидание, но общую попытку не отменяет.
func (b *BFF) verifier(ctx context.Context) (*Verifier, error) {
	if v := b.v.Load(); v != nil {
		return v, nil
	}
	if err := b.discoveryBackoff(); err != nil {
		return nil, err
	}
	attempt := b.group.DoChan("discovery", func() (any, error) {
		if v := b.v.Load(); v != nil {
			return v, nil
		}
		dctx, cancel := discoveryContext(ctx)
		defer cancel()

		v, err := NewVerifier(dctx, RPConfig{
			Issuer:        b.cfg.issuer(),
			ClientID:      b.cfg.ClientID,
			ClientSecret:  b.cfg.ClientSecret,
			RedirectURL:   b.cfg.RedirectURL,
			Leeway:        b.cfg.leeway(),
			AllowInsecure: b.cfg.AllowInsecureIssuer,
		}, b.doer)

		b.mu.Lock()
		b.lastAttempt = time.Now()
		b.lastErr = err
		b.mu.Unlock()
		if err != nil {
			return nil, err
		}
		b.v.Store(v)
		return v, nil
	})
	// Дождавшийся отваливается по своему дедлайну, но попытку не отменяет: её
	// делят все, и она доводится до конца (и до отрицательного кэша).
	select {
	case res := <-attempt:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*Verifier), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("oidckit: discovery: %w", ctx.Err())
	}
}

// discoveryContext — дедлайн общей попытки discovery: отмена одного запроса не
// должна ронять её (WithoutCancel), но дедлайн вызывающего обязана уважать —
// иначе fx-хук старта висит дольше своего таймаута, а разбираться приходится с
// его обезличенной ошибкой вместо ошибки кита.
func discoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := discoveryTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (b *BFF) discoveryBackoff() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastErr != nil && time.Since(b.lastAttempt) < discoveryRetryWindow {
		return b.lastErr
	}
	return nil
}

type redirectOutput struct {
	Location     string        `header:"Location"`
	CacheControl string        `header:"Cache-Control"`
	SetCookie    []http.Cookie `header:"Set-Cookie"`
}

type callbackInput struct {
	Code  string `maxLength:"4096" query:"code"`
	State string `maxLength:"128"  query:"state"`
	// Оба имени flow-cookie объявлены статически (huma читает cookie по
	// константному имени); действует то, что задано политикой COOKIE_INSECURE.
	StateCookieSecure string `cookie:"__Host-oidc_state"`
	StateCookiePlain  string `cookie:"oidc_state"`
}

func (in *callbackInput) flowState(cfg CookieConfig) string {
	if cfg.Secure() {
		return in.StateCookieSecure
	}
	return in.StateCookiePlain
}

type loginInput struct {
	ReturnTo string `maxLength:"2048" query:"return_to"`
}

func (b *BFF) login(ctx context.Context, in *loginInput) (*redirectOutput, error) {
	v, err := b.verifier(ctx)
	if err != nil {
		b.instr.login(ctx, stageLogin, "discovery_error")
		b.log.Error(ctx, "oidc discovery failed", "error", err)
		return nil, huma.Error502BadGateway("oidc discovery failed")
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
		b.instr.login(ctx, stageLogin, "state_store_error")
		b.log.Error(ctx, "oidc state store failed", "error", err)
		return nil, huma.Error502BadGateway("state store unavailable")
	}

	b.instr.login(ctx, stageLogin, "ok")
	return &redirectOutput{
		Location:     v.AuthCodeURL(state, nonce, challenge),
		CacheControl: noStore,
		SetCookie:    []http.Cookie{StateCookie(state, b.cookie, stateTTL)},
	}, nil
}

func (b *BFF) callback(ctx context.Context, in *callbackInput) (*redirectOutput, error) {
	if in.Code == "" || in.State == "" {
		b.instr.login(ctx, stageCallback, "invalid_request")
		return nil, huma.Error400BadRequest("missing code or state")
	}
	// Login-CSRF: callback без flow-cookie этого браузера (или с чужой) —
	// чужой логин, подсунутый жертве.
	flow := in.flowState(b.cookie)
	if subtle.ConstantTimeCompare([]byte(flow), []byte(in.State)) != 1 {
		b.instr.login(ctx, stageCallback, "csrf_mismatch")
		return nil, huma.Error400BadRequest("state does not match login flow")
	}

	raw, ok, err := b.store.GetDel(ctx, stateKeyPrefix+in.State)
	if err != nil {
		b.instr.login(ctx, stageCallback, "state_store_error")
		b.log.Error(ctx, "oidc state store failed", "error", err)
		return nil, huma.Error502BadGateway("state store unavailable")
	}
	if !ok {
		b.instr.login(ctx, stageCallback, "unknown_state")
		return nil, huma.Error400BadRequest("unknown or expired state")
	}

	var st loginState
	if err := json.Unmarshal(raw, &st); err != nil {
		b.instr.login(ctx, stageCallback, "corrupt_state")
		return nil, huma.Error400BadRequest("corrupt state")
	}

	v, err := b.verifier(ctx)
	if err != nil {
		b.instr.login(ctx, stageCallback, "discovery_error")
		b.log.Error(ctx, "oidc discovery failed", "error", err)
		return nil, huma.Error502BadGateway("oidc discovery failed")
	}
	claims, err := v.Exchange(ctx, in.Code, st.CodeVerifier, st.Nonce)
	if err != nil {
		b.instr.login(ctx, stageCallback, "exchange_error")
		b.log.Warn(ctx, "oidc exchange failed", "error", err)
		return nil, huma.Error401Unauthorized("oidc exchange failed")
	}

	if b.hooks.OnLogin != nil {
		if err := b.hooks.OnLogin(ctx, claims); err != nil {
			b.instr.login(ctx, stageCallback, "hook_error")
			b.log.Warn(ctx, "oidc login hook failed", "error", err)
			return nil, huma.Error500InternalServerError("login hook failed")
		}
	}

	session, err := b.signer.Sign(ctx, b.sessionClaims(claims), b.cfg.SessionTTL)
	if err != nil {
		b.instr.login(ctx, stageCallback, "sign_error")
		b.log.Error(ctx, "oidc session sign failed", "error", err)
		return nil, huma.Error500InternalServerError("session sign failed")
	}

	b.instr.login(ctx, stageCallback, "ok")
	return &redirectOutput{
		Location:     b.postLoginURL(st.ReturnTo),
		CacheControl: noStore,
		SetCookie: []http.Cookie{
			SessionCookie(session, b.cookie, b.cfg.SessionTTL),
			expiredCookie(b.cookie.StateCookieName(), b.cookie, http.SameSiteLaxMode),
		},
	}, nil
}

// sessionClaims — claims session-JWT. Служебный typ ставится после хука: по
// нему Extractor отличает cookie-сессию от любого другого токена под тем же
// JWT_SECRET.
func (b *BFF) sessionClaims(c Claims) map[string]any {
	claims := map[string]any{
		"sub":                c.Subject,
		"preferred_username": c.Username,
		"name":               c.Name,
		"email":              c.Email,
	}
	if b.hooks.SessionClaims != nil {
		maps.Copy(claims, b.hooks.SessionClaims(c))
	}
	claims[authnkit.ClaimTokenType] = SessionTokenType
	return claims
}

func (b *BFF) postLoginURL(returnTo string) string {
	if returnTo != "" {
		return returnTo
	}
	return b.cfg.postLoginURL()
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
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Data MeView `json:"data"`
	}
}

func (b *BFF) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	id, ok := IdentityFrom(ctx)
	if !ok || id.Subject == "" {
		return nil, huma.Error401Unauthorized("no sso session")
	}
	out := &meOutput{CacheControl: noStore}
	out.Body.Data = MeView(id)
	return out, nil
}

type logoutInput struct {
	SecFetchSite string `header:"Sec-Fetch-Site"`
	Origin       string `header:"Origin"`
}

type logoutOutput struct {
	CacheControl string        `header:"Cache-Control"`
	SetCookie    []http.Cookie `header:"Set-Cookie"`
}

func (b *BFF) logout(ctx context.Context, in *logoutInput) (*logoutOutput, error) {
	if !b.sameSiteRequest(in) {
		b.instr.login(ctx, stageLogout, "cross_site")
		return nil, huma.Error403Forbidden("cross-site logout")
	}
	b.instr.login(ctx, stageLogout, "ok")
	return &logoutOutput{CacheControl: noStore, SetCookie: ExpiredSessionCookies(b.cookie)}, nil
}

// sameSiteRequest защищает logout от forced-logout формы с чужого сайта:
// браузер помечает такой POST как cross-site (Sec-Fetch-Site) и всегда шлёт
// Origin. Запрос без обоих заголовков — не браузерный, он проходит.
func (b *BFF) sameSiteRequest(in *logoutInput) bool {
	switch strings.ToLower(in.SecFetchSite) {
	case "same-origin", "same-site", "none":
		return true
	case "cross-site":
		return false
	}
	if in.Origin == "" {
		return true
	}
	self := b.cfg.selfOrigin()
	return self != "" && strings.EqualFold(in.Origin, self)
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
		Summary:       "Start OIDC login (redirect to idp)",
	}, b.login)

	huma.Register(api, huma.Operation{
		OperationID:   "oidcCallback",
		Method:        http.MethodGet,
		Path:          p + "/callback",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusFound,
		Summary:       "OIDC callback: exchange code for a session cookie",
	}, b.callback)

	huma.Register(api, huma.Operation{
		OperationID: "oidcMe",
		Method:      http.MethodGet,
		Path:        p + "/me",
		Tags:        []string{"auth"},
		Security:    []map[string][]string{{SecuritySchemeName: {}}},
		Summary:     "Current identity from the session cookie",
	}, b.me)

	huma.Register(api, huma.Operation{
		OperationID:   "oidcLogout",
		Method:        http.MethodPost,
		Path:          p + "/logout",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
		Summary:       "Log out: clear the session cookie",
	}, b.logout)
}
