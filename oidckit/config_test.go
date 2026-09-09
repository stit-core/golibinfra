package oidckit

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Issuer:       "https://idp.example",
		ClientID:     "svc",
		ClientSecret: "cs",
		RedirectURL:  "https://app.example/auth/callback",
		PostLoginURL: "/",
		SessionTTL:   12 * time.Hour,
		Leeway:       time.Minute,
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"полный конфиг", func(*Config) {}, false},
		{"пустой конфиг", func(c *Config) { *c = Config{} }, true},
		{"без issuer", func(c *Config) { c.Issuer = "" }, true},
		{"без client id", func(c *Config) { c.ClientID = "" }, true},
		{"без redirect url", func(c *Config) { c.RedirectURL = "" }, true},
		{"относительный redirect url", func(c *Config) { c.RedirectURL = "/auth/callback" }, true},
		{"issuer по http", func(c *Config) { c.Issuer = "http://idp.example" }, true},
		{"issuer по http с явным разрешением", func(c *Config) {
			c.Issuer = "http://127.0.0.1:9000"
			c.AllowInsecureIssuer = true
		}, false},
		{"issuer без схемы", func(c *Config) { c.Issuer = "idp.example" }, true},
		{"issuer с query", func(c *Config) { c.Issuer = "https://idp.example?x=1" }, true},
		{"нулевой session ttl", func(c *Config) { c.SessionTTL = 0 }, true},
		{"отрицательный session ttl", func(c *Config) { c.SessionTTL = -time.Hour }, true},
		{"отрицательный leeway", func(c *Config) { c.Leeway = -time.Second }, true},
		{"внешний post-login url", func(c *Config) { c.PostLoginURL = "https://evil.example" }, true},
		{"post-login url с обходом", func(c *Config) { c.PostLoginURL = "/\t//evil.example" }, true},
		{"issuer с хвостовым слешем", func(c *Config) { c.Issuer = "https://idp.example/" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate()=%v, ожидали ошибку=%v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrConfig) {
				t.Fatalf("ошибка не оборачивает ErrConfig: %v", err)
			}
		})
	}
}

func TestConfig_Derived(t *testing.T) {
	cfg := validConfig()
	cfg.Issuer = "https://idp.example/"
	if got := cfg.issuer(); got != "https://idp.example" {
		t.Fatalf("issuer()=%q", got)
	}
	if got := cfg.selfOrigin(); got != "https://app.example" {
		t.Fatalf("selfOrigin()=%q", got)
	}
	cfg.Leeway = 0
	if got := cfg.leeway(); got != DefaultLeeway {
		t.Fatalf("leeway()=%v", got)
	}
	cfg.PostLoginURL = ""
	if got := cfg.postLoginURL(); got != "/" {
		t.Fatalf("postLoginURL()=%q", got)
	}
}

func TestConfig_RoutePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/auth"},
		{"/auth", "/auth"},
		{"/sso/", "/sso"},
	}
	for _, tc := range cases {
		if got := (Config{RoutePrefix: tc.in}).routePrefix(); got != tc.want {
			t.Fatalf("routePrefix(%q)=%q, ожидали %q", tc.in, got, tc.want)
		}
	}
}

func TestCookieConfig_ZeroValueIsSecure(t *testing.T) {
	var cfg CookieConfig
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero value невалиден: %v", err)
	}
	if !cfg.Secure() {
		t.Fatal("zero value CookieConfig не Secure")
	}
	if cfg.SessionCookieName() != CookieAccessSecure || cfg.StateCookieName() != CookieStateSecure {
		t.Fatalf("имена cookie без префикса: %s / %s", cfg.SessionCookieName(), cfg.StateCookieName())
	}
	if cfg.sameSite() != http.SameSiteLaxMode {
		t.Fatalf("sameSite=%v, ожидали Lax", cfg.sameSite())
	}

	dev := CookieConfig{Insecure: true}
	if dev.Secure() || dev.SessionCookieName() != CookieAccess || dev.StateCookieName() != CookieState {
		t.Fatalf("dev-политика: %+v", dev)
	}
}

func TestCookieConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     CookieConfig
		wantErr bool
		want    http.SameSite
	}{
		{"Lax", CookieConfig{SameSite: "Lax"}, false, http.SameSiteLaxMode},
		{"strict в нижнем регистре", CookieConfig{SameSite: "strict"}, false, http.SameSiteStrictMode},
		{"None при Secure", CookieConfig{SameSite: "None"}, false, http.SameSiteNoneMode},
		{"None без Secure", CookieConfig{SameSite: "None", Insecure: true}, true, 0},
		{"мусор", CookieConfig{SameSite: "sometimes"}, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate()=%v, ожидали ошибку=%v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrConfig) {
					t.Fatalf("ошибка не оборачивает ErrConfig: %v", err)
				}
				return
			}
			if got := tc.cfg.sameSite(); got != tc.want {
				t.Fatalf("sameSite=%v, ожидали %v", got, tc.want)
			}
		})
	}
}

// Дефолты env-конструкторов — главный safe-default кита.
func TestNewConfig_Defaults(t *testing.T) {
	t.Setenv("OIDC_ISSUER", "https://idp.example")
	t.Setenv("OIDC_CLIENT_ID", "svc")
	t.Setenv("OIDC_REDIRECT_URL", "https://app.example/auth/callback")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.SessionTTL != 12*time.Hour || cfg.Leeway != DefaultLeeway {
		t.Fatalf("дефолты: %+v", cfg)
	}
	if cfg.PostLoginURL != "/" || cfg.AllowInsecureIssuer || !cfg.DiscoveryOnStart {
		t.Fatalf("дефолты: %+v", cfg)
	}
}

func TestNewConfig_InvalidEnvFails(t *testing.T) {
	t.Setenv("OIDC_ISSUER", "https://idp.example")
	// OIDC_CLIENT_ID не задан — частичный конфиг не «выключает» OIDC молча.
	if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("ожидали ErrConfig, получили %v", err)
	}

	// Значение, которое не разбирается самим envconfig, тоже валит конструктор.
	t.Setenv("OIDC_SESSION_TTL", "半часа")
	if _, err := NewConfig(); err == nil {
		t.Fatal("неразбираемый OIDC_SESSION_TTL прошёл")
	}
	t.Setenv("COOKIE_INSECURE", "не-bool")
	if _, err := NewCookieConfig(); err == nil {
		t.Fatal("неразбираемый COOKIE_INSECURE прошёл")
	}
}

func TestNewCookieConfig_Defaults(t *testing.T) {
	cfg, err := NewCookieConfig()
	if err != nil {
		t.Fatalf("NewCookieConfig: %v", err)
	}
	if !cfg.Secure() || cfg.sameSite() != http.SameSiteLaxMode {
		t.Fatalf("дефолты: %+v (secure=%v)", cfg, cfg.Secure())
	}

	t.Setenv("COOKIE_INSECURE", "true")
	t.Setenv("COOKIE_SAMESITE", "Strict")
	dev, err := NewCookieConfig()
	if err != nil {
		t.Fatalf("NewCookieConfig: %v", err)
	}
	if dev.Secure() || dev.sameSite() != http.SameSiteStrictMode {
		t.Fatalf("env не применились: %+v", dev)
	}

	t.Setenv("COOKIE_SAMESITE", "sometimes")
	if _, err := NewCookieConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("ожидали ErrConfig, получили %v", err)
	}
}

func TestSessionCookie_Attributes(t *testing.T) {
	c := SessionCookie("tok", CookieConfig{SameSite: SameSiteStrict}, time.Hour)
	if !c.Secure || !c.HttpOnly || c.Name != CookieAccessSecure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie ослаблена: %+v", c)
	}
	if c.SameSite != http.SameSiteStrictMode || c.MaxAge != 3600 {
		t.Fatalf("cookie: %+v", c)
	}

	// Flow-cookie всегда Lax: callback приходит cross-site top-level переходом.
	state := StateCookie("s", CookieConfig{SameSite: SameSiteStrict}, stateTTL)
	if state.SameSite != http.SameSiteLaxMode || state.Name != CookieStateSecure || !state.HttpOnly {
		t.Fatalf("flow-cookie: %+v", state)
	}

	for _, exp := range ExpiredSessionCookies(CookieConfig{}) {
		if !exp.Secure || exp.MaxAge != -1 || exp.Path != "/" {
			t.Fatalf("expired cookie: %+v", exp)
		}
	}
}
