package amocrmkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// flakyServer отвечает 502 на первый запрос и 200 на последующие.
func flakyServer(t *testing.T, hits *atomic.Int32, ok string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(ok))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Повтор POST-создания создал бы дубль сделки: кит запрещает его независимо от
// HTTPCLIENT_RETRY_NON_IDEMPOTENT.
func TestCreateIsNeverRetried(t *testing.T) {
	var hits atomic.Int32
	srv := flakyServer(t, &hits, `{"_embedded":{"leads":[{"id":1}]}}`)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 3
	hcCfg.RetryNonIdempotent = true
	c := newTestClientCfg(t, Config{RateRPS: 1000, HostOverride: srv.URL}, hcCfg)

	if _, err := c.CreateLeads(context.Background(), testAccount(), []LeadInput{{Name: "x"}}); err == nil {
		t.Fatal("502 without a retry must return an error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1: a POST create must not be repeated", hits.Load())
	}
}

// Тот же запрет — на чужом httpclient, конфиг которого кит не контролирует.
func TestCreateIsNeverRetriedWithInjectedClient(t *testing.T) {
	var hits atomic.Int32
	srv := flakyServer(t, &hits, `{"_embedded":{"contacts":[{"id":1}]}}`)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 3
	hcCfg.RetryNonIdempotent = true
	hc, err := httpclient.New(hcCfg)
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	c, err := New(Config{RateRPS: 1000, HostOverride: srv.URL}, hcCfg, nil, logkit.Nop(), WithHTTPClient(hc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := c.CreateContacts(context.Background(), testAccount(), []ContactInput{{Name: "x"}}); err == nil {
		t.Fatal("502 without a retry must return an error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

// GET идемпотентен по методу — повтор разрешён.
func TestGetIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := flakyServer(t, &hits, `{"_embedded":{"leads":[]}}`)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 2
	c := newTestClientCfg(t, Config{RateRPS: 1000, HostOverride: srv.URL}, hcCfg)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil {
		t.Fatalf("ListLeads: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (GET is retried)", hits.Load())
	}
}

// Ephemeral-вызовы amoJo кит помечает как безопасные для повтора явно.
func TestEphemeralAmojoCallIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := flakyServer(t, &hits, ``)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 2
	c := newTestClientCfg(t, Config{RateRPS: 1000, HostOverride: srv.URL}, hcCfg)

	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: "s"}
	if err := c.AmojoTyping(context.Background(), tgt, "scope-1", map[string]any{"conversation_id": "c"}); err != nil {
		t.Fatalf("AmojoTyping: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (typing is retried)", hits.Load())
	}
}

// Сообщение чата — не ephemeral: повтор продублировал бы сообщение.
func TestAmojoNewMessageIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := flakyServer(t, &hits, `{"new_message":{"msgid":"m"}}`)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 2
	hcCfg.RetryNonIdempotent = true
	c := newTestClientCfg(t, Config{RateRPS: 1000, HostOverride: srv.URL}, hcCfg)

	tgt := AmojoTarget{TLD: "ru", AccountKey: "demo", Secret: "s"}
	if _, err := c.AmojoNewMessage(context.Background(), tgt, "scope-1", NewMessagePayload{MsgID: "m"}); err == nil {
		t.Fatal("502 without a retry must return an error")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}
