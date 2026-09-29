package httpkit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitConfig — token-bucket на клиента; RPS = 0 выключает limiter.
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

// ClientIP — IP клиента для лимитера, аудита и антибрутфорса. С trustProxy
// берётся **правое** значение последнего заголовка X-Forwarded-For — его
// дописал доверенный прокси ($proxy_add_x_forwarded_for), тогда как всё левее
// прислал сам клиент; значение должно парситься как IP, иначе — X-Real-IP,
// иначе — RemoteAddr.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// Прокси, который добавляет свой X-Forwarded-For вместо замены, оставляет
		// первым заголовок клиента: доверять можно только последнему.
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
		if rip := r.Header.Values("X-Real-IP"); len(rip) > 0 {
			if ip := strings.TrimSpace(rip[len(rip)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimit отвечает 429 с Retry-After, когда клиент исчерпал свой bucket.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	if cfg.RPS <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return rateLimitWith(newRateLimiter(cfg, time.Now), newInstruments())
}

func rateLimitWith(l *rateLimiter, instr *instruments) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(ClientIP(r, l.cfg.TrustProxy)) {
				instr.rateLimited.Add(r.Context(), 1)
				writeAPIError(r.Context(), w, Error("RATE_LIMITED", http.StatusTooManyRequests, "rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
