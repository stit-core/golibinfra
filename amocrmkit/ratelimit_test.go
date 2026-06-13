package amocrmkit

import (
	"context"
	"testing"
)

func TestAccountLimiter_PerAccountIsolation(t *testing.T) {
	l := newAccountLimiter(2) // burst = 2 на аккаунт

	la := l.limiterFor("a")
	a1, a2 := la.Allow(), la.Allow() // side-effect: каждый Allow тратит токен
	if !a1 || !a2 {
		t.Fatal("первые 2 слота аккаунта 'a' должны пройти")
	}
	if la.Allow() {
		t.Error("3-й вызов 'a' должен троттлиться")
	}

	// Аккаунт 'b' имеет независимый бюджет.
	lb := l.limiterFor("b")
	b1, b2 := lb.Allow(), lb.Allow()
	if !b1 || !b2 {
		t.Error("аккаунт 'b' должен иметь собственный бюджет 7→2")
	}
}

func TestAccountLimiter_SameAccountSameLimiter(t *testing.T) {
	l := newAccountLimiter(7)
	first := l.limiterFor("x")
	second := l.limiterFor("x")
	if first != second {
		t.Error("один аккаунт → один *rate.Limiter (иначе бюджет не соблюдается)")
	}
}

func TestAccountLimiter_WaitRespectsContext(t *testing.T) {
	l := newAccountLimiter(1) // burst 1
	l.limiterFor("a").Allow() // выбираем единственный токен

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.wait(ctx, "a"); err == nil {
		t.Error("wait на исчерпанном лимите с отменённым ctx должен вернуть ошибку")
	}
}
