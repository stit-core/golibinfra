package httpkit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type RateLimitConfig struct {
	// RPS = 0 выключает limiter.
	RPS   int `envconfig:"HTTP_RATE_LIMIT_RPS" default:"100"`
	Burst int `envconfig:"HTTP_RATE_LIMIT_BURST" default:"200"`
	// TrustProxy — брать IP клиента из X-Forwarded-For / X-Real-IP; только за
	// собственным reverse-proxy, иначе заголовок подделывается.
	TrustProxy bool `envconfig:"HTTP_RATE_LIMIT_TRUST_PROXY" default:"false"`
}

const (
	limiterIdleTTL    = 5 * time.Minute
	limiterSweepEvery = time.Minute
)

type limiterEntry struct {
	l        *rate.Limiter
	lastSeen time.Time
}

// rateLimiter — bucket на IP клиента; простаивающие записи выметаются, чтобы
// скан адресов не растил память без ограничений.
type rateLimiter struct {
	cfg RateLimitConfig
	now func() time.Time

	mu        sync.Mutex
	buckets   map[string]*limiterEntry
	lastSweep time.Time
}

func newRateLimiter(cfg RateLimitConfig, now func() time.Time) *rateLimiter {
	if cfg.Burst <= 0 {
		cfg.Burst = cfg.RPS
	}
	return &rateLimiter{cfg: cfg, now: now, buckets: map[string]*limiterEntry{}, lastSweep: now()}
}

func (l *rateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	e, ok := l.buckets[ip]
	if !ok {
		if t.Sub(l.lastSweep) >= limiterSweepEvery {
			for k, v := range l.buckets {
				if t.Sub(v.lastSeen) > limiterIdleTTL {
					delete(l.buckets, k)
				}
			}
			l.lastSweep = t
		}
		e = &limiterEntry{l: rate.NewLimiter(rate.Limit(l.cfg.RPS), l.cfg.Burst)}
		l.buckets[ip] = e
	}
	e.lastSeen = t
	return e.l.AllowN(t, 1)
}

func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); first != "" {
				return first
			}
		}
		if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
			return rip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	if cfg.RPS <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return rateLimitWith(newRateLimiter(cfg, time.Now), newInstruments())
}

func rateLimitWith(l *rateLimiter, instr *instruments) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(clientIP(r, l.cfg.TrustProxy)) {
				instr.rateLimited.Add(r.Context(), 1)
				w.Header().Set("Retry-After", "1")
				writeAPIError(w, Error("RATE_LIMITED", http.StatusTooManyRequests, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
