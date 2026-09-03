package httpkit

import (
	"net/http"
	"net/http/httptest"
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

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	req.Header.Set("X-Real-IP", "203.0.113.9")

	if got := clientIP(req, false); got != "192.0.2.10" {
		t.Errorf("without proxy trust: %q", got)
	}
	if got := clientIP(req, true); got != "203.0.113.7" {
		t.Errorf("with proxy trust: %q", got)
	}
	req.Header.Del("X-Forwarded-For")
	if got := clientIP(req, true); got != "203.0.113.9" {
		t.Errorf("X-Real-IP fallback: %q", got)
	}
}
