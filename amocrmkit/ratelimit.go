package amocrmkit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// limiterIdleTTL — аккаунт, молчавший дольше, выбрасывается из карты лимитеров.
const limiterIdleTTL = 15 * time.Minute

// accountLimiter — per-account rate-limiter. Потолок amoCRM — rps НА АККАУНТ,
// поэтому ключ — аккаунт (subdomain), не глобально. Бюджет общий для всех
// outbound-вызовов аккаунта (REST + amoJo + Drive + OAuth).
type accountLimiter struct {
	rps   int
	ttl   time.Duration
	clock clockkit.Clock

	mu        sync.Mutex
	m         map[string]*limiterEntry
	lastSweep time.Time
}

type limiterEntry struct {
	lim      *rate.Limiter
	lastUsed time.Time
}

func newAccountLimiter(rps int, ttl time.Duration, clock clockkit.Clock) *accountLimiter {
	return &accountLimiter{rps: rps, ttl: ttl, clock: clock, m: make(map[string]*limiterEntry)}
}

func (l *accountLimiter) limiterFor(account string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.sweepLocked(now)
	e, ok := l.m[account]
	if !ok {
		// burst = rps: допускаем короткий всплеск, дальше — стабильные rps/сек.
		e = &limiterEntry{lim: rate.NewLimiter(rate.Limit(l.rps), l.rps)}
		l.m[account] = e
	}
	e.lastUsed = now
	return e.lim
}

// sweepLocked не чаще раза в ttl удаляет аккаунты, молчавшие дольше ttl: их
// bucket за это время полон, так что удаление ничего не теряет.
func (l *accountLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.ttl {
		return
	}
	l.lastSweep = now
	for account, e := range l.m {
		if now.Sub(e.lastUsed) >= l.ttl {
			delete(l.m, account)
		}
	}
}

func (l *accountLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// wait блокирует до доступного слота либо до отмены ctx.
func (l *accountLimiter) wait(ctx context.Context, account string) error {
	return l.limiterFor(account).Wait(ctx)
}

// beforeAttempt — хук httpclient: повторные попытки тоже проходят через
// лимитер аккаунта (первую ждёт execute до Do).
func (l *accountLimiter) beforeAttempt(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	return l.wait(ctx, accountFrom(ctx))
}

type accountKey struct{}

func withAccount(ctx context.Context, account string) context.Context {
	return context.WithValue(ctx, accountKey{}, account)
}

func accountFrom(ctx context.Context) string {
	v, _ := ctx.Value(accountKey{}).(string)
	return v
}
