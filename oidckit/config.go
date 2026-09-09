package oidckit

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrConfig — невалидный конфиг кита: kit падает на старте, а не на первом запросе.
var ErrConfig = errors.New("oidckit: invalid config")

const (
	// DefaultLeeway — допуск рассинхронизации часов при проверке exp/nbf/iat ID-token'а.
	DefaultLeeway = time.Minute
	// MaxReturnToLen — предел длины `return_to` (он же предел записи в StateStore).
	MaxReturnToLen = 2048
	// MaxStateLen — предел длины `state` в callback-запросе.
	MaxStateLen = 128

	defaultRoutePrefix = "/auth"
)

// Config — env-конфиг BFF-контура (имена OIDC_*/COOKIE_* — public API кита).
type Config struct {
	// Issuer — issuer OIDC-провайдера; из него строится discovery-URL.
	Issuer string `envconfig:"OIDC_ISSUER"`
	// ClientID — идентификатор relying-party у провайдера.
	ClientID string `envconfig:"OIDC_CLIENT_ID"`
	// ClientSecret — секрет relying-party (token endpoint).
	ClientSecret string `envconfig:"OIDC_CLIENT_SECRET"`
	// RedirectURL — абсолютный URL callback-роута сервиса (redirect_uri).
	RedirectURL string `envconfig:"OIDC_REDIRECT_URL"`
	// PostLoginURL — путь редиректа после входа, если нет `return_to`.
	PostLoginURL string `envconfig:"OIDC_POST_LOGIN_URL" default:"/"`
	// SessionTTL — срок жизни cookie-сессии (exp session-JWT и Max-Age cookie).
	SessionTTL time.Duration `envconfig:"OIDC_SESSION_TTL" default:"12h"`
	// Leeway — допуск часов при проверке exp/nbf/iat ID-token'а.
	Leeway time.Duration `envconfig:"OIDC_LEEWAY" default:"1m"`
	// AllowInsecureIssuer разрешает issuer по http (локальный IdP в dev-стенде).
	AllowInsecureIssuer bool `envconfig:"OIDC_ALLOW_INSECURE_ISSUER" default:"false"`
	// DiscoveryOnStart требует успешного discovery на старте приложения.
	DiscoveryOnStart bool `envconfig:"OIDC_DISCOVERY_ON_START" default:"true"`
	// RoutePrefix — префикс BFF-роутов (login/callback/me/logout); программный,
	// не env: пути — часть API-контракта сервиса, пусто → "/auth".
	RoutePrefix string `ignored:"true"`
}

// Validate проверяет конфиг целиком: частичный конфиг не «выключает» OIDC молча,
// а роняет приложение на старте.
func (c Config) Validate() error {
	switch {
	case c.Issuer == "":
		return fmt.Errorf("%w: OIDC_ISSUER is required", ErrConfig)
	case c.ClientID == "":
		return fmt.Errorf("%w: OIDC_CLIENT_ID is required", ErrConfig)
	case c.RedirectURL == "":
		return fmt.Errorf("%w: OIDC_REDIRECT_URL is required", ErrConfig)
	case c.SessionTTL <= 0:
		return fmt.Errorf("%w: OIDC_SESSION_TTL must be > 0", ErrConfig)
	case c.Leeway < 0:
		return fmt.Errorf("%w: OIDC_LEEWAY must not be negative", ErrConfig)
	}
	if err := validateIssuer(c.Issuer, c.AllowInsecureIssuer); err != nil {
		return err
	}
	if err := validateAbsoluteURL("OIDC_REDIRECT_URL", c.RedirectURL); err != nil {
		return err
	}
	if c.PostLoginURL != "" && sanitizeReturnTo(c.PostLoginURL) == "" {
		return fmt.Errorf("%w: OIDC_POST_LOGIN_URL must be a local path starting with /", ErrConfig)
	}
	return nil
}

// issuer — issuer без хвостового слеша: сравнение с discovery-документом и
// claim'ом iss ведётся по нормализованному значению.
func (c Config) issuer() string { return strings.TrimRight(c.Issuer, "/") }

func (c Config) routePrefix() string {
	p := strings.TrimRight(c.RoutePrefix, "/")
	if p == "" {
		return defaultRoutePrefix
	}
	return p
}

func (c Config) leeway() time.Duration {
	if c.Leeway <= 0 {
		return DefaultLeeway
	}
	return c.Leeway
}

// selfOrigin — origin сервиса по RedirectURL; с ним сверяется заголовок Origin
// на logout.
func (c Config) selfOrigin() string {
	u, err := url.Parse(c.RedirectURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (c Config) postLoginURL() string {
	if c.PostLoginURL == "" {
		return "/"
	}
	return c.PostLoginURL
}

func validateIssuer(issuer string, allowInsecure bool) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: OIDC_ISSUER must be an absolute URL without query", ErrConfig)
	}
	if u.Scheme != "https" && (!allowInsecure || u.Scheme != "http") {
		return fmt.Errorf("%w: OIDC_ISSUER must use https (OIDC_ALLOW_INSECURE_ISSUER=true for dev)", ErrConfig)
	}
	return nil
}

func validateAbsoluteURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%w: %s must be an absolute http(s) URL", ErrConfig, name)
	}
	return nil
}

// Имена session-cookie: при Secure действует префикс `__Host-` (RFC 6265bis
// §4.1.3) — такую cookie не может подменить ни поддомен, ни сосед по хосту.
const (
	// CookieAccess — session-cookie без префикса (COOKIE_INSECURE=true, http-dev).
	CookieAccess = "access_token"
	// CookieAccessSecure — session-cookie при Secure.
	CookieAccessSecure = "__Host-access_token"
	// CookieState — flow-cookie логин-флоу без префикса (COOKIE_INSECURE=true).
	CookieState = "oidc_state"
	// CookieStateSecure — flow-cookie логин-флоу при Secure.
	CookieStateSecure = "__Host-oidc_state"
)

// SameSite* — допустимые значения COOKIE_SAMESITE.
const (
	SameSiteLax    = "Lax"
	SameSiteStrict = "Strict"
	SameSiteNone   = "None"
)

// CookieConfig — политика cookie кита. Zero value безопасен: Secure включён,
// SameSite=Lax.
type CookieConfig struct {
	// Insecure снимает флаг Secure — только локальная разработка по http.
	Insecure bool `envconfig:"COOKIE_INSECURE" default:"false"`
	// SameSite — политика SameSite сессионной cookie: Lax | Strict | None.
	SameSite string `envconfig:"COOKIE_SAMESITE" default:"Lax"`
}

// Validate проверяет COOKIE_SAMESITE и его совместимость с COOKIE_INSECURE.
func (c CookieConfig) Validate() error {
	switch strings.ToLower(c.SameSite) {
	case "", strings.ToLower(SameSiteLax), strings.ToLower(SameSiteStrict):
		return nil
	case strings.ToLower(SameSiteNone):
		if c.Insecure {
			return fmt.Errorf("%w: COOKIE_SAMESITE=None requires COOKIE_INSECURE=false", ErrConfig)
		}
		return nil
	default:
		return fmt.Errorf("%w: COOKIE_SAMESITE must be Lax, Strict or None", ErrConfig)
	}
}

// Secure — выставляется ли флаг Secure (и, вместе с ним, префикс `__Host-`).
func (c CookieConfig) Secure() bool { return !c.Insecure }

// SessionCookieName — имя session-cookie при текущей политике.
func (c CookieConfig) SessionCookieName() string {
	if c.Secure() {
		return CookieAccessSecure
	}
	return CookieAccess
}

// StateCookieName — имя flow-cookie логин-флоу при текущей политике.
func (c CookieConfig) StateCookieName() string {
	if c.Secure() {
		return CookieStateSecure
	}
	return CookieState
}

func (c CookieConfig) sameSite() http.SameSite {
	switch strings.ToLower(c.SameSite) {
	case strings.ToLower(SameSiteStrict):
		return http.SameSiteStrictMode
	case strings.ToLower(SameSiteNone):
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
