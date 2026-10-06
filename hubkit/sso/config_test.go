package sso_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/sso"
)

func validConfig() sso.Config {
	return sso.Config{
		JWKSURL:                    "https://id.test/.well-known/jwks.json",
		Cookie:                     sso.DefaultCookie,
		Issuer:                     "https://id.test",
		Audience:                   "stit",
		LoginURL:                   "https://id.test/auth/login",
		Leeway:                     sso.DefaultLeeway,
		KeyGrace:                   sso.DefaultKeyGrace,
		SecuritySchemeName:         sso.DefaultSecuritySchemeName,
		CustomerCookie:             sso.DefaultCustomerCookie,
		CustomerSecuritySchemeName: sso.DefaultCustomerSecuritySchemeName,
	}
}

func TestConfig_Validate(t *testing.T) {
	enableCustomer := func(c *sso.Config) {
		c.CustomerJWKSURL = "https://api.hub.test/.well-known/jwks.json"
		c.CustomerIssuer = "https://api.hub.test"
		c.CustomerAudience = "stit-customer"
		c.CustomerLoginURL = "https://hub.test/customer/login"
	}
	tests := map[string]struct {
		mutate func(*sso.Config)
		ok     bool
	}{
		"employee contour only":     {mutate: func(*sso.Config) {}, ok: true},
		"both contours":             {mutate: enableCustomer, ok: true},
		"no jwks url":               {mutate: func(c *sso.Config) { c.JWKSURL = "" }},
		"relative jwks url":         {mutate: func(c *sso.Config) { c.JWKSURL = "/jwks" }},
		"no audience":               {mutate: func(c *sso.Config) { c.Audience = "" }},
		"no issuer":                 {mutate: func(c *sso.Config) { c.Issuer = "" }},
		"no cookie":                 {mutate: func(c *sso.Config) { c.Cookie = "" }},
		"no login url":              {mutate: func(c *sso.Config) { c.LoginURL = "" }},
		"negative leeway":           {mutate: func(c *sso.Config) { c.Leeway = -time.Second }},
		"negative key grace":        {mutate: func(c *sso.Config) { c.KeyGrace = -time.Second }},
		"no scheme name":            {mutate: func(c *sso.Config) { c.SecuritySchemeName = "" }},
		"customer without issuer":   {mutate: func(c *sso.Config) { enableCustomer(c); c.CustomerIssuer = "" }},
		"customer without audience": {mutate: func(c *sso.Config) { enableCustomer(c); c.CustomerAudience = "" }},
		"customer cookie collision": {mutate: func(c *sso.Config) { enableCustomer(c); c.CustomerCookie = c.Cookie }},
		"scheme name collision": {mutate: func(c *sso.Config) {
			enableCustomer(c)
			c.CustomerSecuritySchemeName = c.SecuritySchemeName
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case !tc.ok && !errors.Is(err, sso.ErrConfig):
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewVerifier_RejectsInvalidConfig(t *testing.T) {
	cfg := validConfig()
	cfg.Issuer = ""
	if _, err := sso.NewVerifier(cfg, nil); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := sso.NewCustomerVerifier(cfg, nil); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("NewCustomerVerifier: %v", err)
	}
}

func TestConfig_SchemeNameDefaults(t *testing.T) {
	var zero sso.Config
	if got := zero.SchemeName(); got != sso.DefaultSecuritySchemeName {
		t.Fatalf("SchemeName() = %q", got)
	}
	if got := zero.CustomerSchemeName(); got != sso.DefaultCustomerSecuritySchemeName {
		t.Fatalf("CustomerSchemeName() = %q", got)
	}
}

func TestNewConfig_ValidatesEnv(t *testing.T) {
	t.Setenv("HUB_SSO_JWKS_URL", "https://id.test/.well-known/jwks.json")
	if _, err := sso.NewConfig(); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("NewConfig without audience: %v", err)
	}
	t.Setenv("HUB_SSO_AUDIENCE", "stit")
	cfg, err := sso.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Leeway != sso.DefaultLeeway || cfg.Cookie != sso.DefaultCookie {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	// Пустой HUB_SSO_ISSUER равен незаданному, явная пустота «-» — отказ конфига.
	t.Setenv("HUB_SSO_ISSUER", "")
	if cfg, err := sso.NewConfig(); err != nil || cfg.Issuer != "https://id.stit.tech" {
		t.Fatalf("NewConfig with HUB_SSO_ISSUER= : %+v, %v, want the default issuer", cfg, err)
	}
	t.Setenv("HUB_SSO_ISSUER", "-")
	if _, err := sso.NewConfig(); !errors.Is(err, sso.ErrConfig) {
		t.Fatalf("NewConfig with HUB_SSO_ISSUER=- : %v, want ErrConfig", err)
	}
}

func TestSecuritySchemes(t *testing.T) {
	cfg := validConfig()
	if got := len(sso.SecuritySchemes(cfg)); got != 1 {
		t.Fatalf("schemes with customer disabled = %d, want 1", got)
	}
	cfg.CustomerJWKSURL = "https://api.hub.test/jwks"
	schemes := sso.SecuritySchemes(cfg)
	if len(schemes) != 2 {
		t.Fatalf("schemes = %d, want 2", len(schemes))
	}
	if schemes[0].Scheme.Name != cfg.Cookie || schemes[1].Scheme.Name != cfg.CustomerCookie {
		t.Fatalf("scheme cookies = %q/%q", schemes[0].Scheme.Name, schemes[1].Scheme.Name)
	}
}
