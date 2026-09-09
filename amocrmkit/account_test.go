package amocrmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// evilAccounts — координаты, из которых конкатенацией получился бы чужой хост.
var evilAccounts = []Account{
	{Subdomain: "evil.com/x?", TLD: "ru"},
	{Subdomain: "demo.amocrm.ru:443@evil.com", TLD: "ru"},
	{Subdomain: "demo", TLD: "com/x?"},
	{Subdomain: "demo", TLD: "ru/../"},
	{Subdomain: "Demo", TLD: "ru"},
	{Subdomain: "", TLD: "ru"},
	{Subdomain: "demo", TLD: ""},
	{Subdomain: "demo", TLD: "net"},
	{Subdomain: "a-very-long-subdomain-that-exceeds-the-sixty-three-character-limit-of-dns", TLD: "ru"},
}

func TestValidSubdomain(t *testing.T) {
	for _, ok := range []string{"demo", "my-company-01", "a", "0"} {
		if !validSubdomain(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "Demo", "demo.ru", "demo/x", "demo?x", "demo x", "демо"} {
		if validSubdomain(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestEvilAccountNeverReachesNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	for _, acc := range evilAccounts {
		acc.Token = "t"
		if _, err := c.ListLeads(ctx, acc, ListParams{}); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("REST %+v: err = %v, want ErrInvalidAccount", acc, err)
		}
		if _, err := c.CreateUploadSession(ctx, acc, "https://drive-b.amocrm.ru", UploadSessionInput{FileName: "f"}); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("Drive %+v: err = %v, want ErrInvalidAccount", acc, err)
		}
		if _, err := c.UploadPart(ctx, acc, "https://drive-b.amocrm.ru/upload", "", nil); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("UploadPart %+v: err = %v, want ErrInvalidAccount", acc, err)
		}
		if _, err := c.ExchangeCode(ctx, acc, testOAuthConfig(), "code"); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("OAuth %+v: err = %v, want ErrInvalidAccount", acc, err)
		}
		if _, err := c.RefreshToken(ctx, acc, testOAuthConfig(), "refresh"); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("OAuth refresh %+v: err = %v, want ErrInvalidAccount", acc, err)
		}
		tgt := AmojoTarget{TLD: acc.TLD, AccountKey: acc.Subdomain, Secret: "s"}
		if err := c.AmojoTyping(ctx, tgt, "scope", map[string]any{}); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("amoJo %+v: err = %v, want ErrInvalidAccount", tgt, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests for invalid accounts", hits.Load())
	}
}

func TestValidAccountBuildsAmocrmHost(t *testing.T) {
	c, err := New(Config{RateRPS: 7}, testHTTPConfig(), clockkit.Real{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	acc := Account{Subdomain: "my-company-01", TLD: "com"}
	if err := acc.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	got, err := c.resolveURL(acc.restBase(), "/oauth2/access_token", nil)
	if err != nil || got.String() != "https://my-company-01.amocrm.com/oauth2/access_token" {
		t.Fatalf("url = %v, %v", got, err)
	}
}

func TestExecuteLogsRateLimitAndServerErrors(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"title":"err","retry_after":30,"trace_id":"tr-1"}`))
	}))
	t.Cleanup(srv.Close)
	log := &captureLogger{}
	c, err := New(Config{RateRPS: 1000, HostOverride: srv.URL}, testHTTPConfig(), nil, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Аккаунт после 429 остаётся оштрафованным, поэтому у каждой фазы свой.
	status.Store(http.StatusTooManyRequests)
	throttled := Account{Subdomain: "throttled", TLD: "ru", Token: "t"}
	if _, err := c.ListLeads(context.Background(), throttled, ListParams{}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if e, ok := log.find("warn", "amocrmkit: rate limited"); !ok || e.kv["account"] != "throttled" || e.kv["retry_after"] != 30 {
		t.Errorf("rate-limited warn = %+v, ok=%v", e, ok)
	}

	status.Store(http.StatusBadGateway)
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); !errors.Is(err, ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
	if e, ok := log.find("warn", "amocrmkit: server error"); !ok || e.kv["status"] != http.StatusBadGateway || e.kv["trace_id"] != "tr-1" {
		t.Errorf("server-error warn = %+v, ok=%v", e, ok)
	}

	status.Store(http.StatusUnauthorized)
	if _, err := c.RefreshToken(context.Background(), testAccount(), testOAuthConfig(), "r"); !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if e, ok := log.find("warn", "amocrmkit: oauth failed"); !ok || e.kv["grant"] != "refresh_token" {
		t.Errorf("oauth warn = %+v, ok=%v", e, ok)
	}
}
