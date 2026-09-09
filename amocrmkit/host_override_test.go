package amocrmkit

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestNewAppliesConfigHostOverride(t *testing.T) {
	c, err := New(Config{RateRPS: 7, HostOverride: "http://fake-amo:8080"}, testHTTPConfig(), clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", url.Values{"limit": {"5"}})
	if err != nil || got.String() != "http://fake-amo:8080/api/v4/leads?limit=5" {
		t.Fatalf("resolveURL = %v, %v; want override host with path and query", got, err)
	}
}

func TestNewNoOverrideUsesAccountHost(t *testing.T) {
	c, err := New(Config{RateRPS: 7}, testHTTPConfig(), clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", nil)
	if err != nil || got.String() != "https://demo.amocrm.ru/api/v4/leads" {
		t.Fatalf("resolveURL = %v, %v; want subdomain host", got, err)
	}
}

func TestHostOverrideKeepsUploadURLPath(t *testing.T) {
	c := newTestClient(t, "http://fake-amo:8080")
	got, err := c.resolveURL("https://drive-b.amocrm.ru/v1.0/sessions/abc/upload?part=2", "", nil)
	if err != nil || got.String() != "http://fake-amo:8080/v1.0/sessions/abc/upload?part=2" {
		t.Fatalf("upload_url under override = %v, %v; path and query must survive", got, err)
	}
	got, err = c.resolveURL("https://drive-b.amocrm.ru/", "/v1.0/files", nil)
	if err != nil || got.String() != "http://fake-amo:8080/v1.0/files" {
		t.Fatalf("drive base with trailing slash = %v, %v", got, err)
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
		"zero rps":         {RateRPS: 0},
		"negative rps":     {RateRPS: -1},
		"negative maxwait": {RateRPS: 7, MaxWait: -time.Second},
		"override host":    {RateRPS: 7, HostOverride: "fake-amo:8080"},
		"override ftp":     {RateRPS: 7, HostOverride: "ftp://fake-amo"},
		"override empty":   {RateRPS: 7, HostOverride: "http://"},
		"override path":    {RateRPS: 7, HostOverride: "http://fake-amo:8080/some/path"},
		"override query":   {RateRPS: 7, HostOverride: "http://fake-amo:8080?x=1"},
		"override user":    {RateRPS: 7, HostOverride: "http://u:p@fake-amo:8080"},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v, want ErrConfig", name, err)
		}
	}
	for _, ok := range []Config{
		{RateRPS: 7},
		{RateRPS: 7, HostOverride: "https://sandbox.example:8443"},
		{RateRPS: 7, HostOverride: "http://fake-amo:8080/"},
		{RateRPS: 7, MaxWait: 0},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("valid config %+v rejected: %v", ok, err)
		}
	}
}
