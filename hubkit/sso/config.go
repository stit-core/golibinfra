package sso

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Default* — значения envconfig-дефолтов, продублированные константами для
// конфигов, собранных литералом мимо NewConfig.
const (
	DefaultCookie                     = "stit_session"
	DefaultCustomerCookie             = "stit_customer"
	DefaultSecuritySchemeName         = "EmployeeSession"
	DefaultCustomerSecuritySchemeName = "CustomerSession"
	DefaultLeeway                     = time.Minute
	DefaultKeyGrace                   = 15 * time.Minute
)

// Config — env-конфиг верификатора hub-сессии (имена HUB_SSO_* — public API).
type Config struct {
	// JWKSURL — JWKS employee-backend; обязателен, абсолютный http(s) URL.
	JWKSURL string `envconfig:"HUB_SSO_JWKS_URL"`
	// Cookie — имя cookie с hub-session JWT сотрудника.
	Cookie string `envconfig:"HUB_SSO_COOKIE" default:"stit_session"`
	// Issuer — ожидаемый iss сотрудника; обязателен (третий барьер к токену
	// чужого контура рядом с aud и kind).
	Issuer string `envconfig:"HUB_SSO_ISSUER" default:"https://id.stit.tech"`
	// Audience — ожидаемый aud; обязателен (= HUB_SESSION_AUDIENCE issuer'а сотрудника).
	Audience string `envconfig:"HUB_SSO_AUDIENCE"`
	// LoginURL — точка входа SSO для login-redirect хелпера.
	LoginURL string `envconfig:"HUB_SSO_LOGIN_URL" default:"https://id.stit.tech/auth/login"`
	// Leeway — допуск часов при проверке exp/nbf/iat.
	Leeway time.Duration `envconfig:"HUB_SSO_LEEWAY" default:"60s"`
	// KeyGrace — сколько выведенный из JWKS kid продолжает проверять подписи
	// после ротации ключа issuer'а; берётся не меньше TTL access-токена, иначе
	// живые токены получают 401 в момент переключения ключа.
	KeyGrace time.Duration `envconfig:"HUB_SSO_KEY_GRACE" default:"15m"`
	// SecuritySchemeName — имя схемы сотрудника в OpenAPI `components.securitySchemes`.
	SecuritySchemeName string `envconfig:"HUB_SSO_SECURITY_SCHEME" default:"EmployeeSession"`

	// CustomerJWKSURL — JWKS company-backend (issuer сессии заказчика). Пусто →
	// контур заказчика выключен: продукт ведёт себя ровно как без него (продукты
	// входят в контур не одновременно).
	CustomerJWKSURL string `envconfig:"HUB_SSO_CUSTOMER_JWKS_URL"`
	// CustomerCookie — имя cookie сессии заказчика (свой слот: сотрудник и
	// заказчик могут быть в одном браузере, один вход не выбивает другой).
	CustomerCookie string `envconfig:"HUB_SSO_CUSTOMER_COOKIE" default:"stit_customer"`
	// CustomerIssuer — ожидаемый iss сессии заказчика; для включённого контура
	// обязателен (токен чужого контура не должен приниматься никогда).
	CustomerIssuer string `envconfig:"HUB_SSO_CUSTOMER_ISSUER" default:"https://api.hub.stit.tech"`
	// CustomerAudience — ожидаемый aud сессии заказчика; для включённого контура
	// обязателен (= HUB_SESSION_AUDIENCE issuer'а заказчика).
	CustomerAudience string `envconfig:"HUB_SSO_CUSTOMER_AUDIENCE"`
	// CustomerLoginURL — экран входа заказчика для login-redirect хелпера.
	CustomerLoginURL string `envconfig:"HUB_SSO_CUSTOMER_LOGIN_URL" default:"https://hub.stit.tech/customer/login"`
	// CustomerSecuritySchemeName — имя схемы заказчика в OpenAPI.
	CustomerSecuritySchemeName string `envconfig:"HUB_SSO_CUSTOMER_SECURITY_SCHEME" default:"CustomerSession"`
}

// CustomerEnabled — включён ли контур заказчика (задан его JWKS).
func (c Config) CustomerEnabled() bool { return c.CustomerJWKSURL != "" }

// SchemeName — имя security-схемы сотрудника: им операция ссылается на схему
// (`Security: []map[string][]string{{cfg.SchemeName(): {}}}`).
func (c Config) SchemeName() string {
	return schemeNameOr(c.SecuritySchemeName, DefaultSecuritySchemeName)
}

// CustomerSchemeName — имя security-схемы заказчика.
func (c Config) CustomerSchemeName() string {
	return schemeNameOr(c.CustomerSecuritySchemeName, DefaultCustomerSecuritySchemeName)
}

// Validate проверяет конфиг верификатора: приложение с наполовину заданным
// контуром не должно стартовать и выяснять это на первом запросе.
func (c Config) Validate() error {
	if err := absoluteURL("HUB_SSO_JWKS_URL", c.JWKSURL, true); err != nil {
		return err
	}
	if c.Audience == "" {
		return fmt.Errorf("%w: HUB_SSO_AUDIENCE is required", ErrConfig)
	}
	if c.Issuer == "" {
		return fmt.Errorf("%w: HUB_SSO_ISSUER is required", ErrConfig)
	}
	if c.Cookie == "" {
		return fmt.Errorf("%w: HUB_SSO_COOKIE is required", ErrConfig)
	}
	if err := absoluteURL("HUB_SSO_LOGIN_URL", c.LoginURL, true); err != nil {
		return err
	}
	if c.Leeway < 0 {
		return fmt.Errorf("%w: HUB_SSO_LEEWAY must be >= 0", ErrConfig)
	}
	if c.KeyGrace < 0 {
		return fmt.Errorf("%w: HUB_SSO_KEY_GRACE must be >= 0", ErrConfig)
	}
	if c.SecuritySchemeName == "" {
		return fmt.Errorf("%w: HUB_SSO_SECURITY_SCHEME is required", ErrConfig)
	}
	return c.validateCustomer()
}

func (c Config) validateCustomer() error {
	if !c.CustomerEnabled() {
		return nil
	}
	if err := absoluteURL("HUB_SSO_CUSTOMER_JWKS_URL", c.CustomerJWKSURL, true); err != nil {
		return err
	}
	if c.CustomerIssuer == "" {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_ISSUER is required when the customer contour is enabled", ErrConfig)
	}
	if c.CustomerAudience == "" {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_AUDIENCE is required when the customer contour is enabled", ErrConfig)
	}
	if c.CustomerCookie == "" {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_COOKIE is required", ErrConfig)
	}
	// Общая cookie на два контура означала бы, что вход заказчика выбивает
	// сессию сотрудника в том же браузере.
	if c.CustomerCookie == c.Cookie {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_COOKIE must differ from HUB_SSO_COOKIE", ErrConfig)
	}
	if err := absoluteURL("HUB_SSO_CUSTOMER_LOGIN_URL", c.CustomerLoginURL, true); err != nil {
		return err
	}
	if c.CustomerSecuritySchemeName == "" {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_SECURITY_SCHEME is required", ErrConfig)
	}
	if c.CustomerSecuritySchemeName == c.SecuritySchemeName {
		return fmt.Errorf("%w: HUB_SSO_CUSTOMER_SECURITY_SCHEME must differ from HUB_SSO_SECURITY_SCHEME", ErrConfig)
	}
	return nil
}

// LoginURLFor строит URL входа сотрудника с возвратом в продукт:
// <LoginURL>?rd=<returnTo>. Пустой returnTo → голый LoginURL (без query).
func (c Config) LoginURLFor(returnTo string) string { return withReturnTo(c.LoginURL, returnTo) }

// CustomerLoginURLFor — то же для экрана входа заказчика.
func (c Config) CustomerLoginURLFor(returnTo string) string {
	return withReturnTo(c.CustomerLoginURL, returnTo)
}

func withReturnTo(base, returnTo string) string {
	if returnTo == "" {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "rd=" + url.QueryEscape(returnTo)
}

func absoluteURL(name, raw string, required bool) error {
	if raw == "" {
		if required {
			return fmt.Errorf("%w: %s is required", ErrConfig, name)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrConfig, name, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: %s must be an absolute http(s) URL", ErrConfig, name)
	}
	return nil
}
