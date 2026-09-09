package jwks_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

// Отзыв ключа не меняет kid активного набора: без перечитывания по времени
// снятый ключ оставался бы доверенным до перезапуска процесса.
func TestKey_StaleSetIsReReadOnHit(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"), jwk(t, "k2"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k2")) // отзыв: k1 выведен из набора

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("внутри окна свежести ключ обязан остаться рабочим: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("загрузок внутри окна = %d, want 1", got)
	}

	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k1"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("после окна свежести отозванный kid = %v, want ErrUnknownKey", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("загрузок = %d, want 2 (перечитывание по времени)", got)
	}
}

// Простой JWKS не должен ронять проверку живых токенов: известный ключ
// переживает неудачное перечитывание.
func TestKey_KnownKeySurvivesFailedReRead(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock))
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusBadGateway)
	clock.Advance(jwks.DefaultMinRefresh)

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("известный ключ при недоступном JWKS: %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("загрузок = %d, want 2 (попытка перечитать была)", got)
	}
}

// Cache-Control: max-age задаёт и период перечитывания, а не только троттлинг.
func TestRespectCacheControl_SetsReReadPeriod(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	ks.setCacheControl("public, max-age=300")
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.WithClock(clock), jwks.WithRespectCacheControl())
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("max-age=300 не удержал перечитывание: %d загрузок", got)
	}

	clock.Advance(5 * time.Minute)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("после max-age ожидали перечитывание: %d загрузок", got)
	}
}
