package amocrmkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// limiterIdleTTL — аккаунт, молчавший дольше, выбрасывается из карты лимитеров.
const limiterIdleTTL = 15 * time.Minute

// accountRef — координаты аккаунта без секретов: ключ лимитера и значения labels.
type accountRef struct {
	Subdomain string
	TLD       string
}

// key — bucket на аккаунт целиком: demo.amocrm.ru и demo.amocrm.com — разные аккаунты.
func (a accountRef) key() string { return a.Subdomain + "." + a.TLD }

// accountLimiter — per-account rate-limiter. Потолок amoCRM — rps НА АККАУНТ,
// бюджет общий для всех outbound-вызовов аккаунта (REST + amoJo + Drive + OAuth).
type accountLimiter struct {
	rps     int
	ttl     time.Duration
	maxWait time.Duration
	clock   clockkit.Clock

	mu        sync.Mutex
	m         map[string]*limiterEntry
	lastSweep time.Time
}

type limiterEntry struct {
	lim         *rate.Limiter
	lastUsed    time.Time
	pausedUntil time.Time
}

func newAccountLimiter(rps int, maxWait, ttl time.Duration, clock clockkit.Clock) *accountLimiter {
	return &accountLimiter{rps: rps, ttl: ttl, maxWait: maxWait, clock: clock, m: make(map[string]*limiterEntry)}
}

func (l *accountLimiter) entryFor(key string) *limiterEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.sweepLocked(now)
	e, ok := l.m[key]
	if !ok {
		// burst = rps: допускаем короткий всплеск, дальше — стабильные rps/сек.
		e = &limiterEntry{lim: rate.NewLimiter(rate.Limit(l.rps), l.rps)}
		l.m[key] = e
	}
	e.lastUsed = now
	return e
}

func (l *accountLimiter) limiterFor(key string) *rate.Limiter { return l.entryFor(key).lim }

// sweepLocked не чаще раза в ttl удаляет аккаунты, молчавшие дольше ttl: их
// bucket за это время полон, так что удаление ничего не теряет.
func (l *accountLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.ttl {
		return
	}
	l.lastSweep = now
	for key, e := range l.m {
		if now.Sub(e.lastUsed) >= l.ttl && !now.Before(e.pausedUntil) {
			delete(l.m, key)
		}
	}
}

func (l *accountLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// penalize отодвигает следующий вызов аккаунта на d: retry_after из ответа 429
// сильнее локального bucket'а, иначе ретраи httpclient добивают аккаунт до 403.
func (l *accountLimiter) penalize(key string, d time.Duration) {
	if d <= 0 {
		return
	}
	until := l.clock.Now().Add(d)
	e := l.entryFor(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(e.pausedUntil) {
		e.pausedUntil = until
	}
}

// pausedFor — сколько осталось до конца штрафа аккаунта (0, если штрафа нет).
func (l *accountLimiter) pausedFor(key string) time.Duration {
	e := l.entryFor(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	return e.pausedUntil.Sub(l.clock.Now())
}

// wait ждёт слот аккаунта: сначала штраф после 429, затем токен bucket'а.
// Ожидание дольше maxWait — ErrRateLimited без запроса; отмена ctx — её ошибка.
func (l *accountLimiter) wait(ctx context.Context, key string) error {
	if pause := l.pausedFor(key); pause > 0 {
		if l.maxWait > 0 && pause > l.maxWait {
			return fmt.Errorf("%w: account paused for %s", ErrRateLimited, pause.Round(time.Second))
		}
		if err := l.sleep(ctx, pause); err != nil {
			return err
		}
	}
	lim := l.limiterFor(key)
	if l.maxWait > 0 {
		r := lim.Reserve()
		if !r.OK() {
			return fmt.Errorf("%w: no slot available", ErrRateLimited)
		}
		if d := r.Delay(); d > l.maxWait {
			r.Cancel()
			return fmt.Errorf("%w: slot in %s", ErrRateLimited, d.Round(time.Millisecond))
		} else if d > 0 {
			if err := l.sleep(ctx, d); err != nil {
				r.Cancel()
				return err
			}
		}
		return nil
	}
	return lim.Wait(ctx)
}

func (l *accountLimiter) sleep(ctx context.Context, d time.Duration) error {
	t := l.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C():
		return nil
	}
}

// beforeAttempt — хук httpclient: повторные попытки тоже проходят через
// лимитер аккаунта (первую ждёт execute до Do).
func (l *accountLimiter) beforeAttempt(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	return l.wait(ctx, accountFrom(ctx).key())
}

type accountKey struct{}

func withAccount(ctx context.Context, acc accountRef) context.Context {
	return context.WithValue(ctx, accountKey{}, acc)
}

func accountFrom(ctx context.Context) accountRef {
	v, _ := ctx.Value(accountKey{}).(accountRef)
	return v
}
