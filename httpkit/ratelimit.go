package httpkit

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"git.stit.tech/stit-core/golibinfra/internal/clientip"
)

// RateLimitConfig — token-bucket на клиента; RPS = 0 выключает limiter.
type RateLimitConfig struct {
	// RPS = 0 выключает limiter.
	RPS   int `envconfig:"HTTP_RATE_LIMIT_RPS" default:"100"`
	Burst int `envconfig:"HTTP_RATE_LIMIT_BURST" default:"200"`
}

const (
	limiterIdleTTL    = 5 * time.Minute
	limiterSweepEvery = time.Minute
	// limiterMaxClients — потолок bucket'ов (≈170 B каждый): поток новых адресов не растит
	// память без предела, сверх него новые клиенты делят один bucket до выметания.
	limiterMaxClients = 100_000
	// limiterIPv6Prefix — свёртка IPv6, как у bruteforcekit по умолчанию: /64 — один клиент.
	limiterIPv6Prefix = 64
)

type limiterEntry struct {
	l        *rate.Limiter
	lastSeen time.Time
}

// rateLimiter — bucket на ключ клиента; простаивающие записи выметаются, а их число
// ограничено limiterMaxClients.
type rateLimiter struct {
	cfg        RateLimitConfig
	now        func() time.Time
	maxClients int

	mu        sync.Mutex
	buckets   map[netip.Addr]*limiterEntry
	overflow  *rate.Limiter
	lastSweep time.Time
}

func newRateLimiter(cfg RateLimitConfig, now func() time.Time) *rateLimiter {
	if cfg.Burst <= 0 {
		cfg.Burst = cfg.RPS
	}
	return &rateLimiter{
		cfg: cfg, now: now, maxClients: limiterMaxClients,
		buckets:   map[netip.Addr]*limiterEntry{},
		overflow:  rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst),
		lastSweep: now(),
	}
}

func (l *rateLimiter) allow(key netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	e, ok := l.buckets[key]
	if !ok {
		if t.Sub(l.lastSweep) >= limiterSweepEvery {
			for k, v := range l.buckets {
				if t.Sub(v.lastSeen) > limiterIdleTTL {
					delete(l.buckets, k)
				}
			}
			l.lastSweep = t
		}
		if len(l.buckets) >= l.maxClients {
			return l.overflow.AllowN(t, 1)
		}
		e = &limiterEntry{l: rate.NewLimiter(rate.Limit(l.cfg.RPS), l.cfg.Burst)}
		l.buckets[key] = e
	}
	e.lastSeen = t
	return e.l.AllowN(t, 1)
}

func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// RateLimit отвечает 429 с Retry-After, когда клиент исчерпал свой bucket. Клиент — IP из
// ClientIP (без него — адрес соединения), IPv6 — по /64.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	if cfg.RPS <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return rateLimitWith(newRateLimiter(cfg, time.Now), newInstruments())
}

func rateLimitWith(l *rateLimiter, instr *instruments) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr := clientip.From(r.Context())
			if !addr.IsValid() {
				addr = remoteAddr(r)
			}
			if !l.allow(clientip.Key(addr, limiterIPv6Prefix)) {
				instr.rateLimited.Add(r.Context(), 1)
				writeAPIError(r.Context(), w, Error("RATE_LIMITED", http.StatusTooManyRequests, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
