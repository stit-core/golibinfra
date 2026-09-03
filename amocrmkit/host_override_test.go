package amocrmkit

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestNewAppliesConfigHostOverride(t *testing.T) {
	hc, _ := httpclient.New(httpclient.Config{Timeout: time.Second})
	c, err := New(Config{RateRPS: 7, HostOverride: "http://fake-amo:8080"}, hc, clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.hostOverride != "http://fake-amo:8080" {
		t.Fatalf("hostOverride = %q, want config value", c.hostOverride)
	}
	got := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", nil)
	if got != "http://fake-amo:8080/api/v4/leads" {
		t.Fatalf("resolveURL = %q, want override host", got)
	}
}

func TestNewNoOverrideUsesAccountHost(t *testing.T) {
	hc, _ := httpclient.New(httpclient.Config{Timeout: time.Second})
	c, err := New(Config{RateRPS: 7}, hc, clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := c.resolveURL(Account{Subdomain: "demo", TLD: "ru"}.restBase(), "/api/v4/leads", nil)
	if got != "https://demo.amocrm.ru/api/v4/leads" {
		t.Fatalf("resolveURL = %q, want subdomain host", got)
	}
}
