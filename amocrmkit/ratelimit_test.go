package amocrmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func newTestLimiter(rps int, ttl time.Duration, clock clockkit.Clock) *accountLimiter {
	return newAccountLimiter(rps, 0, ttl, clock)
}

func TestAccountLimiter_PerAccountIsolation(t *testing.T) {
	l := newTestLimiter(2, limiterIdleTTL, clockkit.Real{}) // burst = 2 на аккаунт

	la := l.limiterFor("a.ru")
	a1, a2 := la.Allow(), la.Allow() // side-effect: каждый Allow тратит токен
	if !a1 || !a2 {
		t.Fatal("the first 2 slots of account 'a' must pass")
	}
	if la.Allow() {
		t.Error("the 3rd call of 'a' must be throttled")
	}

	lb := l.limiterFor("b.ru")
	b1, b2 := lb.Allow(), lb.Allow()
	if !b1 || !b2 {
		t.Error("account 'b' must have a budget of its own")
	}
}

func TestAccountLimiter_SameAccountSameLimiter(t *testing.T) {
	l := newTestLimiter(7, limiterIdleTTL, clockkit.Real{})
	first := l.limiterFor("x.ru")
	if second := l.limiterFor("x.ru"); first != second {
		t.Error("one account must map to one *rate.Limiter, otherwise the budget is not enforced")
	}
}

// demo.amocrm.ru и demo.amocrm.com — разные аккаунты с разными бюджетами.
func TestAccountLimiter_KeyIncludesTLD(t *testing.T) {
	ru := accountRef{Subdomain: "demo", TLD: "ru"}
	com := accountRef{Subdomain: "demo", TLD: "com"}
	if ru.key() == com.key() {
		t.Fatalf("keys collided: %q", ru.key())
	}
	l := newTestLimiter(7, limiterIdleTTL, clockkit.Real{})
	if l.limiterFor(ru.key()) == l.limiterFor(com.key()) {
		t.Error("accounts in different TLDs share a bucket")
	}
}

func TestAccountLimiter_WaitRespectsContext(t *testing.T) {
	l := newTestLimiter(1, limiterIdleTTL, clockkit.Real{}) // burst 1
	l.limiterFor("a.ru").Allow()                            // выбираем единственный токен

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx, "a.ru"); err == nil {
		t.Error("wait on an exhausted limit with a canceled ctx must return an error")
	}
}

func TestAccountLimiter_EvictsIdleAccounts(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newTestLimiter(7, time.Minute, clk)

	l.limiterFor("idle.ru")
	clk.Advance(30 * time.Second)
	l.limiterFor("active.ru")
	clk.Advance(40 * time.Second) // idle молчит 70s > ttl, active — 40s < ttl
	l.limiterFor("newcomer.ru")

	if l.size() != 2 {
		t.Fatalf("size = %d, want 2 (idle evicted, active + newcomer kept)", l.size())
	}
	l.mu.Lock()
	_, idle := l.m["idle.ru"]
	_, active := l.m["active.ru"]
	l.mu.Unlock()
	if idle || !active {
		t.Fatalf("idle=%v active=%v; want idle evicted and active kept", idle, active)
	}
}

func TestAccountLimiter_SweepNotMoreOftenThanTTL(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newTestLimiter(7, time.Minute, clk)

	l.limiterFor("a.ru")
	clk.Advance(30 * time.Second)
	l.limiterFor("b.ru") // первый sweep был при создании "a": повторный ещё рано
	clk.Advance(45 * time.Second)
	l.limiterFor("c.ru") // 75s с последнего sweep: "a" молчит 75s → evicted, "b" 45s → kept
	if l.size() != 2 {
		t.Fatalf("size = %d, want 2", l.size())
	}
}

// Оштрафованный аккаунт не выбрасывается sweep'ом: иначе пауза после 429 исчезнет.
func TestAccountLimiter_KeepsPausedAccountsOnSweep(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newTestLimiter(7, time.Minute, clk)

	l.penalize("a.ru", time.Hour)
	clk.Advance(2 * time.Minute)
	l.limiterFor("b.ru")
	if l.size() != 2 {
		t.Fatalf("size = %d, want 2 (paused account kept)", l.size())
	}
}

func TestAccountLimiter_BeforeAttemptSkipsFirstAndWaitsOnRetry(t *testing.T) {
	l := newTestLimiter(1, limiterIdleTTL, clockkit.Real{})
	acc := accountRef{Subdomain: "a", TLD: "ru"}
	l.limiterFor(acc.key()).Allow() // бюджет исчерпан

	ctx, cancel := context.WithCancel(withAccount(context.Background(), acc))
	cancel()
	if err := l.beforeAttempt(ctx, 0); err != nil {
		t.Fatalf("attempt 0 must not wait (execute already did): %v", err)
	}
	if err := l.beforeAttempt(ctx, 1); err == nil {
		t.Fatal("retry attempt must wait on the account limiter")
	}
}

// Пауза дольше MaxWait отклоняет вызов локально — без запроса и без блокировки горутины.
func TestAccountLimiter_PenaltyOverMaxWaitFailsFast(t *testing.T) {
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	l := newAccountLimiter(7, time.Second, limiterIdleTTL, clk)
	l.penalize("a.ru", 5*time.Minute)

	if err := l.wait(context.Background(), "a.ru"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	clk.Advance(5 * time.Minute)
	if err := l.wait(context.Background(), "a.ru"); err != nil {
		t.Fatalf("the call must pass once the pause is over: %v", err)
	}
}

// Очередь за слотом ограничена MaxWait: 10k вызовов аккаунта не висят минутами.
func TestAccountLimiter_QueueBoundedByMaxWait(t *testing.T) {
	l := newAccountLimiter(1, 10*time.Millisecond, limiterIdleTTL, clockkit.Real{})
	if err := l.wait(context.Background(), "a.ru"); err != nil {
		t.Fatalf("first slot: %v", err)
	}
	if err := l.wait(context.Background(), "a.ru"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited (the slot in 1s is over MaxWait)", err)
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
	c := newTestClientCfg(t, Config{RateRPS: 10, HostOverride: srv.URL}, hcCfg)
	for c.rl.limiterFor(testAccount().ref().key()).Allow() {
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

// 429 с retry_after в теле: ретраи httpclient и следующие вызовы аккаунта не
// уходят в сеть, пока пауза не истечёт.
func Test429BodyRetryAfterPausesAccount(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"title":"Too Many Requests","status":429,"retry_after":300}`))
	}))
	t.Cleanup(srv.Close)

	hcCfg := testHTTPConfig()
	hcCfg.MaxRetries = 2
	hcCfg.RetryWait = time.Millisecond
	hcCfg.RetryMaxWait = 5 * time.Millisecond
	c := newTestClientCfg(t, Config{RateRPS: 1000, MaxWait: 50 * time.Millisecond, HostOverride: srv.URL}, hcCfg)

	start := time.Now()
	for i := range 3 {
		_, err := c.ListLeads(context.Background(), testAccount(), ListParams{})
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("call %d: err = %v, want ErrRateLimited", i, err)
		}
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("elapsed %s: the calls waited out the pause instead of failing fast", el)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1: retries and later calls must back off for retry_after", hits.Load())
	}
	if pause := c.rl.pausedFor(testAccount().ref().key()); pause < 4*time.Minute {
		t.Errorf("pause = %s, want ~5m from retry_after", pause)
	}
}

// Retry-After заголовком (в т.ч. дробным) — тот же штраф, что и полем тела.
func Test429HeaderRetryAfterPausesAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1.5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := newTestClientCfg(t, Config{RateRPS: 1000, MaxWait: time.Millisecond, HostOverride: srv.URL}, testHTTPConfig())
	_, err := c.ListLeads(context.Background(), testAccount(), ListParams{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 2 {
		t.Fatalf("RetryAfter = %+v, want 2 (1.5s rounded up)", apiErr)
	}
	if pause := c.rl.pausedFor(testAccount().ref().key()); pause <= 0 {
		t.Errorf("the account is not penalized: pause = %s", pause)
	}
}

func TestRetryAfterSeconds_Formats(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := map[string]int{
		"":              0,
		"30":            30,
		"1.5":           2,
		"-5":            0,
		"garbage":       0,
		"9999999999999": maxRetryAfterSeconds,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	}
	for v, want := range cases {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		if got := retryAfterSeconds(h, now); got != want {
			t.Errorf("retryAfterSeconds(%q) = %d, want %d", v, got, want)
		}
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
	c, err := New(Config{RateRPS: 10, HostOverride: srv.URL}, testHTTPConfig(), nil, nil, WithHTTPClient(hc))
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

// Конфиг httpclient без таймаута (в том числе httpclient.Config{}) — ErrConfig: клиента без
// таймаута кит не собирает.
func TestNewValidatesHTTPClientConfig(t *testing.T) {
	for _, hc := range []httpclient.Config{{Timeout: -time.Second}, {}} {
		if _, err := New(Config{RateRPS: 7}, hc, nil, logkit.Nop()); !errors.Is(err, ErrConfig) {
			t.Fatalf("httpclient %+v: err = %v, want ErrConfig", hc, err)
		}
	}
	if _, err := New(Config{RateRPS: 7}, testHTTPConfig(), nil, logkit.Nop()); err != nil {
		t.Fatalf("New: %v", err)
	}
}
