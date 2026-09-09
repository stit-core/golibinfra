package issuer_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
)

func TestCookieConfig_Validate(t *testing.T) {
	valid := issuer.CookieConfig{
		Name: issuer.DefaultCookieName, Domain: issuer.DefaultCookieDomain, Secure: true,
		TTL: issuer.DefaultSessionTTL, RefreshName: issuer.DefaultRefreshCookieName,
		RefreshPath: issuer.DefaultRefreshCookiePath,
	}
	tests := map[string]struct {
		mutate func(*issuer.CookieConfig)
		ok     bool
	}{
		"valid":           {mutate: func(*issuer.CookieConfig) {}, ok: true},
		"no name":         {mutate: func(c *issuer.CookieConfig) { c.Name = "" }},
		"no refresh name": {mutate: func(c *issuer.CookieConfig) { c.RefreshName = "" }},
		"same names":      {mutate: func(c *issuer.CookieConfig) { c.RefreshName = c.Name }},
		"relative path":   {mutate: func(c *issuer.CookieConfig) { c.RefreshPath = "auth" }},
		"zero ttl":        {mutate: func(c *issuer.CookieConfig) { c.TTL = 0 }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case !tc.ok && !errors.Is(err, issuer.ErrConfig):
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestRefreshConfig_Validate(t *testing.T) {
	valid := issuer.RefreshConfig{TTL: time.Hour, TombstoneTTL: time.Hour, ReuseGrace: issuer.DefaultReuseGrace}
	tests := map[string]struct {
		mutate func(*issuer.RefreshConfig)
		ok     bool
	}{
		"valid":            {mutate: func(*issuer.RefreshConfig) {}, ok: true},
		"minimal grace":    {mutate: func(c *issuer.RefreshConfig) { c.ReuseGrace = time.Second }, ok: true},
		"zero ttl":         {mutate: func(c *issuer.RefreshConfig) { c.TTL = 0 }},
		"short tombstone":  {mutate: func(c *issuer.RefreshConfig) { c.TombstoneTTL = time.Minute }},
		"zero grace":       {mutate: func(c *issuer.RefreshConfig) { c.ReuseGrace = 0 }},
		"sub-second grace": {mutate: func(c *issuer.RefreshConfig) { c.ReuseGrace = 100 * time.Millisecond }},
		"grace over ttl":   {mutate: func(c *issuer.RefreshConfig) { c.ReuseGrace = 2 * time.Hour }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case !tc.ok && !errors.Is(err, issuer.ErrConfig):
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewRefreshConfig_RejectsZeroReuseGrace(t *testing.T) {
	t.Setenv("HUB_REFRESH_REUSE_GRACE", "0")
	if _, err := issuer.NewRefreshConfig(); !errors.Is(err, issuer.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewConfigs_FromEnv(t *testing.T) {
	t.Setenv("HUB_SESSION_AUDIENCE", "stit")
	t.Setenv("HUB_SESSION_PRIVATE_KEY", genPEM(t))
	key, err := issuer.NewKeyConfig()
	if err != nil {
		t.Fatalf("NewKeyConfig: %v", err)
	}
	if key.Issuer != "https://id.stit.tech" || key.JWKSMaxAge != issuer.DefaultJWKSMaxAge {
		t.Fatalf("key defaults: %+v", key)
	}

	cookie, err := issuer.NewCookieConfig()
	if err != nil {
		t.Fatalf("NewCookieConfig: %v", err)
	}
	if cookie.Name != issuer.DefaultCookieName || cookie.TTL != issuer.DefaultSessionTTL || !cookie.Secure {
		t.Fatalf("cookie defaults: %+v", cookie)
	}

	refresh, err := issuer.NewRefreshConfig()
	if err != nil {
		t.Fatalf("NewRefreshConfig: %v", err)
	}
	if refresh.TTL != 720*time.Hour || refresh.ReuseGrace != issuer.DefaultReuseGrace {
		t.Fatalf("refresh defaults: %+v", refresh)
	}
}

func TestNewKeyConfig_FailsWithoutAudience(t *testing.T) {
	t.Setenv("HUB_SESSION_PRIVATE_KEY", genPEM(t))
	if _, err := issuer.NewKeyConfig(); !errors.Is(err, issuer.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewRefreshConfig_RejectsShortTombstone(t *testing.T) {
	t.Setenv("HUB_REFRESH_TTL", "24h")
	t.Setenv("HUB_REFRESH_TOMBSTONE_TTL", "1h")
	if _, err := issuer.NewRefreshConfig(); !errors.Is(err, issuer.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}
