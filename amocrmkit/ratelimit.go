package amocrmkit

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// accountLimiter — per-account rate-limiter. Потолок amoCRM — 7 rps НА АККАУНТ,
// поэтому ключ — аккаунт (subdomain), не глобально. Бюджет общий для всех
// outbound-вызовов аккаунта (REST + amoJo + Drive); оркестрация (poller vs
// команды) — у gateway.
type accountLimiter struct {
	rps int
	mu  sync.Mutex
	m   map[string]*rate.Limiter
}

func newAccountLimiter(rps int) *accountLimiter {
	return &accountLimiter{rps: rps, m: make(map[string]*rate.Limiter)}
}

func (l *accountLimiter) limiterFor(account string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.m[account]
	if !ok {
		// burst = rps: допускаем короткий всплеск, дальше — стабильные rps/сек.
		lim = rate.NewLimiter(rate.Limit(l.rps), l.rps)
		l.m[account] = lim
	}
	return lim
}

// wait блокирует до доступного слота либо до отмены ctx.
func (l *accountLimiter) wait(ctx context.Context, account string) error {
	return l.limiterFor(account).Wait(ctx)
}
