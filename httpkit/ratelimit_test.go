package httpkit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRateLimiter_EvictsIdleClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 10}, func() time.Time { return now })

	for i := range 1000 {
		l.allow("10.0.0." + string(rune('0'+i%10)) + ":" + string(rune(i)))
	}
	if l.size() != 1000 {
		t.Fatalf("size before sweep: %d", l.size())
	}

	now = now.Add(limiterIdleTTL + limiterSweepEvery)
	l.allow("fresh")
	if l.size() != 1 {
		t.Fatalf("idle clients must be evicted, size=%d", l.size())
	}
}

func TestRateLimiter_BurstDefaultsToRPS(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 3, Burst: 0}, func() time.Time { return now })
	allowed := 0
	for range 5 {
		if l.allow("c") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("Burst=0 must fall back to RPS: allowed %d", allowed)
	}
}

// Регрессия: доверенный прокси дописывает реальный IP справа, поэтому левое
// значение X-Forwarded-For — подделка клиента и ключом лимитера быть не должно.
func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	req.Header.Set("X-Real-IP", "203.0.113.9")

	if got := ClientIP(req, false); got != "192.0.2.10" {
		t.Errorf("without proxy trust: %q", got)
	}
	if got := ClientIP(req, true); got != "10.0.0.1" {
		t.Errorf("with proxy trust the rightmost XFF value wins: %q", got)
	}

	req.Header.Set("X-Forwarded-For", "10.0.0.1, not-an-ip")
	if got := ClientIP(req, true); got != "203.0.113.9" {
		t.Errorf("non-IP XFF must fall back to X-Real-IP: %q", got)
	}
	req.Header.Del("X-Forwarded-For")
	if got := ClientIP(req, true); got != "203.0.113.9" {
		t.Errorf("X-Real-IP fallback: %q", got)
	}
	req.Header.Set("X-Real-IP", "spoofed")
	if got := ClientIP(req, true); got != "192.0.2.10" {
		t.Errorf("non-IP X-Real-IP must fall back to RemoteAddr: %q", got)
	}
}

// Регрессия (NF2): прокси, который **добавляет** свой заголовок вместо замены
// (HAProxy add-header, часть sidecar'ов), оставляет заголовок клиента первым —
// доверять можно только последнему.
func TestClientIP_SecondForwardedForHeaderWins(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Add("X-Forwarded-For", "203.0.113.7, 198.51.100.77")
	req.Header.Add("X-Forwarded-For", "10.0.0.1")
	if got := ClientIP(req, true); got != "10.0.0.1" {
		t.Errorf("the value appended by the proxy must win: %q", got)
	}

	req.Header.Del("X-Forwarded-For")
	req.Header.Add("X-Real-IP", "198.51.100.77")
	req.Header.Add("X-Real-IP", "10.0.0.2")
	if got := ClientIP(req, true); got != "10.0.0.2" {
		t.Errorf("the last X-Real-IP must win: %q", got)
	}
}

// Регрессия (NF2): подделка через второй заголовок не создаёт новых bucket'ов.
func TestRateLimit_ExtraForwardedForHeaderIsNotForgeable(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, TrustProxy: true}, func() time.Time { return now })
	h := rateLimitWith(l, newInstruments())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var last *httptest.ResponseRecorder
	for i := range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.9:1234"
		req.Header.Add("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		req.Header.Add("X-Forwarded-For", "203.0.113.5")
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("a client-supplied extra XFF header must not create new buckets: got %d", last.Code)
	}
	if l.size() != 1 {
		t.Fatalf("buckets = %d, want 1", l.size())
	}
}

func TestRateLimit_KeyIsNotForgeableBehindProxy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1, TrustProxy: true}, func() time.Time { return now })
	h := rateLimitWith(l, newInstruments())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var last *httptest.ResponseRecorder
	for i := range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.9:1234"
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i)+", 203.0.113.5")
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("client-supplied XFF prefix must not create new buckets: got %d", last.Code)
	}
	if l.size() != 1 {
		t.Fatalf("buckets = %d, want 1", l.size())
	}
}
