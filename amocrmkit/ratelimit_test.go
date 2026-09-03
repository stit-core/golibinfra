package amocrmkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestAccountLimiter_PerAccountIsolation(t *testing.T) {
	l := newAccountLimiter(2, limiterIdleTTL, clockkit.Real{}) // burst = 2 на аккаунт

	la := l.limiterFor("a")
	a1, a2 := la.Allow(), la.Allow() // side-effect: каждый Allow тратит токен
	if !a1 || !a2 {
		t.Fatal("первые 2 слота аккаунта 'a' должны пройти")
	}
	if la.Allow() {
		t.Error("3-й вызов 'a' должен троттлиться")
	}

	// Аккаунт 'b' имеет независимый бюджет.
	lb := l.limiterFor("b")
	b1, b2 := lb.Allow(), lb.Allow()
	if !b1 || !b2 {
		t.Error("аккаунт 'b' должен иметь собственный бюджет")
	}
}

func TestAccountLimiter_SameAccountSameLimiter(t *testing.T) {
	l := newAccountLimiter(7, limiterIdleTTL, clockkit.Real{})
	first := l.limiterFor("x")
	second := l.limiterFor("x")
	if first != second {
		t.Error("один аккаунт → один *rate.Limiter (иначе бюджет не соблюдается)")
	}
}

func TestAccountLimiter_WaitRespectsContext(t *testing.T) {
	l := newAccountLimiter(1, limiterIdleTTL, clockkit.Real{}) // burst 1
	l.limiterFor("a").Allow()                                  // выбираем единственный токен

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx, "a"); err == nil {
		t.Error("wait на исчерпанном лимите с отменённым ctx должен вернуть ошибку")
	}
}

func TestAccountLimiter_EvictsIdleAccounts(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newAccountLimiter(7, time.Minute, clk)

	l.limiterFor("idle")
	clk.Advance(30 * time.Second)
	l.limiterFor("active")
	clk.Advance(40 * time.Second) // idle молчит 70s > ttl, active — 40s < ttl
	l.limiterFor("newcomer")

	if l.size() != 2 {
		t.Fatalf("size = %d, want 2 (idle evicted, active + newcomer kept)", l.size())
	}
	l.mu.Lock()
	_, idle := l.m["idle"]
	_, active := l.m["active"]
	l.mu.Unlock()
	if idle || !active {
		t.Fatalf("idle=%v active=%v; want idle evicted and active kept", idle, active)
	}
}

func TestAccountLimiter_SweepNotMoreOftenThanTTL(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newAccountLimiter(7, time.Minute, clk)

	l.limiterFor("a")
	clk.Advance(30 * time.Second)
	l.limiterFor("b") // первый sweep был при создании "a": повторный ещё рано
	clk.Advance(45 * time.Second)
	l.limiterFor("c") // 75s с последнего sweep: "a" молчит 75s → evicted, "b" 45s → kept
	if l.size() != 2 {
		t.Fatalf("size = %d, want 2", l.size())
	}
}

func TestAccountLimiter_BeforeAttemptSkipsFirstAndWaitsOnRetry(t *testing.T) {
	l := newAccountLimiter(1, limiterIdleTTL, clockkit.Real{})
	l.limiterFor("a").Allow() // бюджет исчерпан

	ctx, cancel := context.WithCancel(withAccount(context.Background(), "a"))
	cancel()
	if err := l.beforeAttempt(ctx, 0); err != nil {
		t.Fatalf("attempt 0 must not wait (execute already did): %v", err)
	}
	if err := l.beforeAttempt(ctx, 1); err == nil {
		t.Fatal("retry attempt must wait on the account limiter")
	}
}

// Ретрай httpclient проходит через лимитер аккаунта: с исчерпанным бюджетом
// (10 rps → слот раз в 100ms) две попытки занимают не меньше двух слотов.
func TestRetriesGoThroughAccountLimiter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"_embedded":{"leads":[]}}`))
	}))
	t.Cleanup(srv.Close)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 1
	c, err := New(Config{RateRPS: 10, HostOverride: srv.URL}, hcCfg, clockkit.Real{}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for c.rl.limiterFor("demo").Allow() {
	}

	start := time.Now()
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil {
		t.Fatalf("ListLeads: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (one retry)", hits.Load())
	}
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("elapsed %s: the retry bypassed the account limiter", el)
	}
}

func TestInjectedHTTPClientIsUsedAsIs(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"_embedded":{"leads":[]}}`))
	}))
	t.Cleanup(srv.Close)

	hc, err := httpclient.New(testHTTPConfig())
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	c, err := New(Config{RateRPS: 10, HostOverride: srv.URL}, httpclient.Config{}, nil, nil, WithHTTPClient(hc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.hc != hc {
		t.Fatal("injected httpclient must be used as is")
	}
	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil || hits.Load() != 1 {
		t.Fatalf("ListLeads via injected client: err=%v hits=%d", err, hits.Load())
	}
}
