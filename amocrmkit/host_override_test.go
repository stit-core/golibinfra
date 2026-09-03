package amocrmkit

import (
	"errors"
	"net/url"
	"testing"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestNewAppliesConfigHostOverride(t *testing.T) {
	c, err := New(Config{RateRPS: 7, HostOverride: "http://fake-amo:8080"}, testHTTPConfig(), clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", url.Values{"limit": {"5"}})
	if err != nil || got != "http://fake-amo:8080/api/v4/leads?limit=5" {
		t.Fatalf("resolveURL = %q, %v; want override host with path and query", got, err)
	}
}

func TestNewNoOverrideUsesAccountHost(t *testing.T) {
	c, err := New(Config{RateRPS: 7}, testHTTPConfig(), clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", nil)
	if err != nil || got != "https://demo.amocrm.ru/api/v4/leads" {
		t.Fatalf("resolveURL = %q, %v; want subdomain host", got, err)
	}
}

func TestHostOverrideKeepsUploadURLPath(t *testing.T) {
	c := newTestClient(t, "http://fake-amo:8080")
	got, err := c.resolveURL("https://drive-b.amocrm.ru/v1.0/sessions/abc/upload?part=2", "", nil)
	if err != nil || got != "http://fake-amo:8080/v1.0/sessions/abc/upload?part=2" {
		t.Fatalf("upload_url under override = %q, %v; path and query must survive", got, err)
	}
	got, err = c.resolveURL("https://drive-b.amocrm.ru/", "/v1.0/files", nil)
	if err != nil || got != "http://fake-amo:8080/v1.0/files" {
		t.Fatalf("drive base with trailing slash = %q, %v", got, err)
	}
}

func TestResolveURLRejectsRelative(t *testing.T) {
	c := newTestClient(t, "http://fake-amo:8080")
	if _, err := c.resolveURL("drive-b.amocrm.ru", "/v1.0/files", nil); err == nil {
		t.Fatal("relative base must be rejected")
	}
}

func TestConfigValidate(t *testing.T) {
	cases := map[string]Config{
		"zero rps":       {RateRPS: 0},
		"negative rps":   {RateRPS: -1},
		"override host":  {RateRPS: 7, HostOverride: "fake-amo:8080"},
		"override ftp":   {RateRPS: 7, HostOverride: "ftp://fake-amo"},
		"override empty": {RateRPS: 7, HostOverride: "http://"},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v, want ErrConfig", name, err)
		}
	}
	if err := (Config{RateRPS: 7, HostOverride: "https://sandbox.example:8443"}).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
