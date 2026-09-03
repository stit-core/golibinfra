package issuer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
)

func refreshManager(t *testing.T, cache cachekit.Cache) *issuer.RefreshManager {
	t.Helper()
	m, err := issuer.NewRefreshManager(cache, issuer.RefreshConfig{TTL: time.Hour, TombstoneTTL: time.Hour})
	if err != nil {
		t.Fatalf("NewRefreshManager: %v", err)
	}
	return m
}

func sampleSession() issuer.Session {
	return issuer.Session{SID: "sess-1", EmployeeID: "emp-1", Subject: "sub-1", Username: "n.sycheva", Role: "employee"}
}

func TestRefresh_RotateInvalidatesOld(t *testing.T) {
	m := refreshManager(t, cachekit.NewMemoryCache())
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sess, newTok, err := m.Rotate(ctx, tok)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if sess.SID != "sess-1" || sess.EmployeeID != "emp-1" {
		t.Fatalf("session не восстановлена: %+v", sess)
	}
	if newTok == tok {
		t.Fatal("rotating refresh: новый токен совпал со старым")
	}
	// Новый токен рабочий (цепочка продолжается).
	if _, _, err := m.Rotate(ctx, newTok); err != nil {
		t.Fatalf("ротация нового токена: %v", err)
	}
}

// Повторное предъявление погашенного токена — признак утечки: отзывается вся
// цепочка (sid), включая ещё не использованный свежий токен.
func TestRefresh_ReuseRevokesFamily(t *testing.T) {
	m := refreshManager(t, cachekit.NewMemoryCache())
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, newTok, err := m.Rotate(ctx, tok)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshReused) {
		t.Fatalf("повтор погашенного токена: ожидали ErrRefreshReused, получили %v", err)
	}
	if _, _, err := m.Rotate(ctx, newTok); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("свежий токен после reuse: ожидали ErrRefreshRevoked, получили %v", err)
	}
	// Третье предъявление того же старого токена — по-прежнему reuse, не «неизвестен».
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshReused) {
		t.Fatalf("третий повтор: ожидали ErrRefreshReused, получили %v", err)
	}
}

func TestRefresh_UnknownToken(t *testing.T) {
	m := refreshManager(t, cachekit.NewMemoryCache())
	if _, _, err := m.Rotate(context.Background(), "never-issued"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("ожидали ErrRefreshInvalid, получили %v", err)
	}
}

func TestRefresh_TombstoneOnLogout(t *testing.T) {
	m := refreshManager(t, cachekit.NewMemoryCache())
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := m.Revoke(ctx, "sess-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("ожидали ErrRefreshRevoked, получили %v", err)
	}
}

// В кэше лежит только sha256 токена: утечка дампа Redis не отдаёт живые токены.
func TestRefresh_StoresHashedKeys(t *testing.T) {
	cache := cachekit.NewMemoryCache()
	m := refreshManager(t, cache)
	ctx := context.Background()

	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, "hub:refresh:"+tok); ok {
		t.Fatal("сырой токен лежит в кэше")
	}
	sum := sha256.Sum256([]byte(tok))
	if _, ok, _ := cache.Get(ctx, "hub:refresh:"+hex.EncodeToString(sum[:])); !ok {
		t.Fatal("записи по sha256 токена нет")
	}
}

func TestRefresh_Expired(t *testing.T) {
	m, err := issuer.NewRefreshManager(cachekit.NewMemoryCache(), issuer.RefreshConfig{TTL: 10 * time.Millisecond, TombstoneTTL: time.Hour})
	if err != nil {
		t.Fatalf("NewRefreshManager: %v", err)
	}
	ctx := context.Background()
	tok, err := m.Issue(ctx, sampleSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := m.Rotate(ctx, tok); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("протухший refresh: ожидали ErrRefreshInvalid, получили %v", err)
	}
}

func TestNewRefreshManager_Config(t *testing.T) {
	cases := map[string]issuer.RefreshConfig{
		"zero TTL":            {TTL: 0, TombstoneTTL: time.Hour},
		"tombstone below TTL": {TTL: time.Hour, TombstoneTTL: time.Minute},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.NewRefreshManager(cachekit.NewMemoryCache(), cfg); !errors.Is(err, issuer.ErrRefreshConfig) {
				t.Fatalf("ожидали ErrRefreshConfig, получили %v", err)
			}
		})
	}
}
