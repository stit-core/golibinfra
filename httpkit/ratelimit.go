package httpkit

import (
	"net"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

type RateLimitConfig struct {
	RPS   int `envconfig:"HTTP_RATE_LIMIT_RPS" default:"100"`
	Burst int `envconfig:"HTTP_RATE_LIMIT_BURST" default:"200"`
}

func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	if cfg.RPS == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]*rate.Limiter)
	)

	limiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		l, ok := buckets[ip]
		if !ok {
			l = rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst)
			buckets[ip] = l
		}
		return l
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			if !limiter(ip).Allow() {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
