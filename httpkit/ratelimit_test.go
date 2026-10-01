package httpkit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestRateLimiter_EvictsIdleClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 10}, func() time.Time { return now })

	for i := range 1000 {
		l.allow(netip.AddrFrom4([4]byte{10, 0, byte(i / 256), byte(i)}))
	}
	if l.size() != 1000 {
		t.Fatalf("size before sweep: %d", l.size())
	}

	now = now.Add(limiterIdleTTL + limiterSweepEvery)
	l.allow(netip.MustParseAddr("192.0.2.1"))
	if l.size() != 1 {
		t.Fatalf("idle clients must be evicted, size=%d", l.size())
	}
}

func TestRateLimiter_BurstDefaultsToRPS(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 3, Burst: 0}, func() time.Time { return now })
	allowed := 0
	for range 5 {
		if l.allow(netip.MustParseAddr("192.0.2.1")) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("Burst=0 must fall back to RPS: allowed %d", allowed)
	}
}

// Число bucket'ов ограничено: сверх предела новые клиенты делят один bucket, а старые
// сохраняют свои; после выметания простаивающих новые снова получают свой.
func TestRateLimiter_MaxClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1}, func() time.Time { return now })
	l.maxClients = 2
	a, b := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	if !l.allow(a) || !l.allow(b) {
		t.Fatal("first requests of known clients must pass")
	}
	if !l.allow(netip.MustParseAddr("192.0.2.3")) || l.allow(netip.MustParseAddr("192.0.2.4")) {
		t.Fatal("over the limit new clients share one bucket")
	}
	if l.size() != 2 {
		t.Fatalf("buckets = %d, want the limit 2", l.size())
	}
	now = now.Add(limiterIdleTTL + limiterSweepEvery)
	if !l.allow(netip.MustParseAddr("192.0.2.5")) || l.size() != 1 {
		t.Fatalf("after the sweep a new client gets its own bucket: size %d", l.size())
	}
}

func rateLimited(t *testing.T, hops, rps int) http.Handler {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return ClientIP(logkit.Nop(), hops)(RateLimit(RateLimitConfig{RPS: rps, Burst: rps})(ok))
}

// H11: IPv6-клиент ротирует адрес в своей /64 — ключ лимитера один.
func TestRateLimit_IPv6ByPrefix(t *testing.T) {
	h := rateLimited(t, 0, 2)
	allowed := 0
	for i := range 100 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = fmt.Sprintf("[2001:db8:1:2::%x]:5555", i+1)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("one /64 got %d requests through a Burst of 2", allowed)
	}
}

// Клиент не выбирает ключ заголовками: за одним прокси (HOPS=1) берётся запись прокси, а
// всё левее — в том числе отдельным заголовком — прислал клиент.
func TestRateLimit_KeyIsNotForgeableBehindProxy(t *testing.T) {
	h := rateLimited(t, 1, 1)
	var last *httptest.ResponseRecorder
	for i := range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.9:1234"
		req.Header.Add("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		req.Header.Add("X-Forwarded-For", "203.0.113.5")
		req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("client-supplied X-Forwarded-For and X-Real-IP must not create new buckets: got %d", last.Code)
	}
}

// Без ClientIP в цепочке лимитер ключует по адресу соединения.
func TestRateLimit_StandaloneUsesPeer(t *testing.T) {
	h := RateLimit(RateLimitConfig{RPS: 1, Burst: 1})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	codes := make([]int, 0, 2)
	for _, xff := range []string{"198.51.100.1", "198.51.100.2"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.10:5555"
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [200 429]", codes)
	}
}
