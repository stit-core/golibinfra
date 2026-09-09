package issuer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/hubkit/hubclaims"
	"git.stit.tech/stit-core/golibinfra/hubkit/issuer"
)

func refreshConfig() issuer.RefreshConfig {
	return issuer.RefreshConfig{TTL: time.Hour, TombstoneTTL: time.Hour, ReuseGrace: issuer.DefaultReuseGrace}
}

func newManager(t *testing.T, cache cachekit.Cache, opts ...issuer.Option) *issuer.RefreshManager {
	t.Helper()
	m, err := issuer.NewRefreshManager(cache, refreshConfig(), opts...)
	if err != nil {
		t.Fatalf("NewRefreshManager: %v", err)
	}
	return m
}

func testSession() issuer.Session {
	return issuer.Session{SID: "sess-1", Subject: "sub-1", EmployeeID: "emp-1"}
}

func TestIssue_CustomerRequiresCompanyID(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	sess := issuer.Session{SID: "sess-c", Subject: "customer-account-1", Kind: hubclaims.KindCustomer}
	if _, err := m.Issue(t.Context(), sess); !errors.Is(err, issuer.ErrNoCompany) {
		t.Fatalf("err = %v, want ErrNoCompany", err)
	}
	sess.CompanyID = "01980000-0000-7000-8000-0000000000a1"
	token, err := m.Issue(t.Context(), sess)
	if err != nil {
		t.Fatalf("Issue with company_id: %v", err)
	}
	got, _, err := m.Rotate(t.Context(), token)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if got.CompanyID != sess.CompanyID || got.Kind != hubclaims.KindCustomer {
		t.Fatalf("session after rotate: %+v", got)
	}
}

func TestRefresh_RotateInvalidatesOld(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	old, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sess, fresh, err := m.Rotate(t.Context(), old)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if sess.SID != "sess-1" || fresh == old {
		t.Fatalf("rotate result: %+v %q", sess, fresh)
	}
	if _, _, err := m.Rotate(t.Context(), fresh); err != nil {
		t.Fatalf("rotate fresh token: %v", err)
	}
}

func TestRefresh_ReuseAfterGraceRevokesFamily(t *testing.T) {
	clock := clockkit.NewTickable(time.Now())
	m := newManager(t, cachekit.NewMemoryCache(cachekit.WithClock(clock)), issuer.WithClock(clock))
	old, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, fresh, err := m.Rotate(t.Context(), old)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	clock.Advance(issuer.DefaultReuseGrace + time.Second)
	if _, _, err := m.Rotate(t.Context(), old); !errors.Is(err, issuer.ErrRefreshReused) {
		t.Fatalf("replay err = %v, want ErrRefreshReused", err)
	}
	if _, _, err := m.Rotate(t.Context(), fresh); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("family token after reuse: %v, want ErrRefreshRevoked", err)
	}
}

func TestRefresh_ParallelRotateDoesNotRevoke(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	const rounds = 3000

	var ok, inFlight, other atomic.Int64
	for range rounds {
		token, err := m.Issue(t.Context(), testSession())
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			fresh []string
		)
		for range 2 {
			wg.Go(func() {
				_, next, err := m.Rotate(context.Background(), token)
				switch {
				case err == nil:
					ok.Add(1)
					mu.Lock()
					fresh = append(fresh, next)
					mu.Unlock()
				case errors.Is(err, issuer.ErrRefreshInFlight):
					inFlight.Add(1)
				default:
					other.Add(1)
				}
			})
		}
		wg.Wait()

		// Токен победителя обязан остаться рабочим: гонка двух вкладок не
		// является утечкой и не отзывает семейство.
		for _, f := range fresh {
			if _, _, err := m.Rotate(t.Context(), f); err != nil {
				t.Fatalf("winner token invalidated by the race: %v", err)
			}
		}
	}

	if ok.Load() != rounds {
		t.Fatalf("successful rotations = %d, want %d", ok.Load(), rounds)
	}
	if other.Load() != 0 {
		t.Fatalf("unexpected outcomes = %d (want only ErrRefreshInFlight for the loser)", other.Load())
	}
	if inFlight.Load() != rounds {
		t.Fatalf("in-flight outcomes = %d, want %d", inFlight.Load(), rounds)
	}
}

func TestRefresh_UnknownToken(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	if _, _, err := m.Rotate(t.Context(), "never-issued"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("err = %v, want ErrRefreshInvalid", err)
	}
}

func TestRefresh_UnknownTokenLeavesNoReservation(t *testing.T) {
	cache := cachekit.NewMemoryCache()
	m := newManager(t, cache)
	if _, _, err := m.Rotate(t.Context(), "never-issued"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("first attempt: %v", err)
	}
	// Резерв снят: повтор того же мусора снова «неизвестный токен», а не reuse.
	if _, _, err := m.Rotate(t.Context(), "never-issued"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("second attempt: %v", err)
	}
}

func TestRefresh_TombstoneOnLogout(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	token, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := m.Revoke(t.Context(), "sess-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, err := m.Rotate(t.Context(), token); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("err = %v, want ErrRefreshRevoked", err)
	}
	if err := m.Revoke(t.Context(), ""); !errors.Is(err, issuer.ErrNoSession) {
		t.Fatalf("Revoke(\"\") = %v, want ErrNoSession", err)
	}
}

func TestRefresh_StoresHashedTokensOnly(t *testing.T) {
	cache := &recordingCache{Cache: cachekit.NewMemoryCache()}
	m := newManager(t, cache)
	token, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := m.Rotate(t.Context(), token); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	for _, key := range cache.keys() {
		if strings.Contains(key, token) {
			t.Fatalf("raw token in cache key %q", key)
		}
	}
}

func TestRefresh_Expired(t *testing.T) {
	clock := clockkit.NewTickable(time.Now())
	cache := cachekit.NewMemoryCache(cachekit.WithClock(clock))
	m, err := issuer.NewRefreshManager(cache, issuer.RefreshConfig{
		TTL: time.Minute, TombstoneTTL: time.Minute, ReuseGrace: time.Second,
	}, issuer.WithClock(clock))
	if err != nil {
		t.Fatalf("NewRefreshManager: %v", err)
	}
	token, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, _, err := m.Rotate(t.Context(), token); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("err = %v, want ErrRefreshInvalid", err)
	}
}

func TestRefresh_FailingCacheIsFailClosed(t *testing.T) {
	boom := errors.New("redis down")
	m := newManager(t, &failingCache{Cache: cachekit.NewMemoryCache(), err: boom})
	if _, err := m.Issue(t.Context(), testSession()); !errors.Is(err, boom) {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := m.Rotate(t.Context(), "any"); !errors.Is(err, boom) {
		t.Fatalf("Rotate: %v", err)
	}
	if err := m.Revoke(t.Context(), "sess-1"); !errors.Is(err, boom) {
		t.Fatalf("Revoke: %v", err)
	}
}

func TestRefresh_ReuseRevokesDespiteClientDisconnect(t *testing.T) {
	clock := clockkit.NewTickable(time.Now())
	cache := &ctxAwareCache{Cache: cachekit.NewMemoryCache(cachekit.WithClock(clock))}
	m := newManager(t, cache, issuer.WithClock(clock))

	old, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, fresh, err := m.Rotate(t.Context(), old)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	clock.Advance(issuer.DefaultReuseGrace + time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache.cancelOnUsedGet = cancel
	if _, _, err := m.Rotate(ctx, old); !errors.Is(err, issuer.ErrRefreshReused) {
		t.Fatalf("replay err = %v, want ErrRefreshReused", err)
	}
	cache.cancelOnUsedGet = nil

	if _, _, err := m.Rotate(t.Context(), fresh); !errors.Is(err, issuer.ErrRefreshRevoked) {
		t.Fatalf("семейство пережило детект утечки на оборванном соединении: %v", err)
	}
}

func TestRefresh_RequiresSession(t *testing.T) {
	m := newManager(t, cachekit.NewMemoryCache())
	if _, err := m.Issue(t.Context(), issuer.Session{Subject: "sub-1"}); !errors.Is(err, issuer.ErrNoSession) {
		t.Fatalf("no sid: %v", err)
	}
	if _, err := m.Issue(t.Context(), issuer.Session{SID: "s1"}); !errors.Is(err, issuer.ErrNoSession) {
		t.Fatalf("no subject: %v", err)
	}
}

func TestNewRefreshManager_Config(t *testing.T) {
	tests := map[string]issuer.RefreshConfig{
		"zero ttl":        {TTL: 0, TombstoneTTL: time.Hour},
		"short tombstone": {TTL: time.Hour, TombstoneTTL: time.Minute},
		"negative grace":  {TTL: time.Hour, TombstoneTTL: time.Hour, ReuseGrace: -time.Second},
		"grace over ttl":  {TTL: time.Minute, TombstoneTTL: time.Hour, ReuseGrace: time.Hour},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.NewRefreshManager(cachekit.NewMemoryCache(), cfg); !errors.Is(err, issuer.ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
	if _, err := issuer.NewRefreshManager(nil, refreshConfig()); !errors.Is(err, issuer.ErrConfig) {
		t.Fatalf("nil cache: %v", err)
	}
}

// recordingCache запоминает ключи, которыми пользовался менеджер.
type recordingCache struct {
	cachekit.Cache
	mu   sync.Mutex
	seen []string
}

func (c *recordingCache) record(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, key)
}

func (c *recordingCache) keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *recordingCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	c.record(key)
	return c.Cache.Get(ctx, key)
}

func (c *recordingCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	c.record(key)
	return c.Cache.Set(ctx, key, value, ttl)
}

func (c *recordingCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.record(key)
	return c.Cache.SetNX(ctx, key, value, ttl)
}

func (c *recordingCache) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	c.record(key)
	return c.Cache.GetDel(ctx, key)
}

// failingCache отвечает ошибкой на каждую операцию записи/чтения.
// ctxAwareCache уважает отмену ctx на записи (как настоящий Redis-клиент) и
// отменяет его сразу после чтения маркера повтора: так выглядит клиент,
// оборвавший соединение в момент детекта утечки.
type ctxAwareCache struct {
	cachekit.Cache
	cancelOnUsedGet context.CancelFunc
}

func (c *ctxAwareCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, ok, err := c.Cache.Get(ctx, key)
	if c.cancelOnUsedGet != nil && strings.Contains(key, ":used:") {
		c.cancelOnUsedGet()
	}
	return v, ok, err
}

func (c *ctxAwareCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.Cache.Set(ctx, key, value, ttl)
}

type failingCache struct {
	cachekit.Cache
	err error
}

func (c *failingCache) Get(context.Context, string) ([]byte, bool, error) { return nil, false, c.err }

func (c *failingCache) Set(context.Context, string, []byte, time.Duration) error { return c.err }

func (c *failingCache) SetNX(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, c.err
}

func (c *failingCache) GetDel(context.Context, string) ([]byte, bool, error) {
	return nil, false, c.err
}

func TestRefresh_IncompleteExchangeIsNotReuse(t *testing.T) {
	clock := clockkit.NewTickable(time.Now())
	// Резерв остаётся висеть (снятие «не срабатывает»): маркер без sid значит,
	// что обмен не дошёл до выдачи — отзывать нечего.
	cache := &noReleaseCache{Cache: cachekit.NewMemoryCache(cachekit.WithClock(clock))}
	m := newManager(t, cache, issuer.WithClock(clock))

	if _, _, err := m.Rotate(t.Context(), "garbage"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("first attempt: %v", err)
	}
	clock.Advance(issuer.DefaultReuseGrace + time.Second)
	if _, _, err := m.Rotate(t.Context(), "garbage"); !errors.Is(err, issuer.ErrRefreshInvalid) {
		t.Fatalf("second attempt: %v, want ErrRefreshInvalid", err)
	}
}

func TestRefresh_CorruptMarker(t *testing.T) {
	cache := cachekit.NewMemoryCache()
	m := newManager(t, cache)
	token, err := m.Issue(t.Context(), testSession())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Ключ маркера строится тем же хешем, что и ключ записи: занимаем его битым JSON.
	sum := sha256.Sum256([]byte(token))
	if err := cache.Set(t.Context(), "hub:refresh:used:"+hex.EncodeToString(sum[:]), []byte("{"), time.Hour); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, _, err := m.Rotate(t.Context(), token); err == nil || !strings.Contains(err.Error(), "corrupt refresh marker") {
		t.Fatalf("err = %v, want corrupt marker", err)
	}
}

// noReleaseCache не снимает собственный резерв: имитирует обмен, оборвавшийся
// между резервом и выдачей нового токена.
type noReleaseCache struct{ cachekit.Cache }

func (c *noReleaseCache) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, nil
}
