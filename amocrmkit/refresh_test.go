package amocrmkit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

type memStore struct {
	mu      sync.Mutex
	pair    TokenPair
	saves   int
	loads   int
	loadErr error
	saveErr error
}

func (s *memStore) Load(context.Context, Account) (TokenPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.pair, s.loadErr
}

func (s *memStore) Save(_ context.Context, _ Account, p TokenPair) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.pair = p
	return nil
}

func (s *memStore) snapshot() (TokenPair, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pair, s.saves
}

// refreshServer выдаёт новую пару и считает сетевые обмены; блокируется, пока
// release не закрыт, чтобы конкурентные вызовы гарантированно наложились.
func refreshServer(t *testing.T, hits *atomic.Int32, release <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if release != nil {
			<-release
		}
		_, _ = w.Write([]byte(`{"expires_in":86400,"server_time":1700000000,"access_token":"fresh","refresh_token":"r2"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Refresh-токен amoCRM одноразовый: 10 параллельных обновлений одного аккаунта
// должны стоить один сетевой обмен и одно сохранение.
func TestRefreshCoordinator_SingleFlight(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := refreshServer(t, &hits, release)

	c := newTestClient(t, srv.URL)
	store := &memStore{pair: TokenPair{AccessToken: "old", RefreshToken: "r1", ExpiresIn: 3600, ServerTime: 1}}
	rc := NewRefreshCoordinator(c, store, testOAuthConfig())

	var wg sync.WaitGroup
	errs := make([]error, 10)
	pairs := make([]TokenPair, 10)
	for i := range 10 {
		wg.Go(func() {
			pairs[i], errs[i] = rc.Refresh(context.Background(), testAccount())
		})
	}
	time.Sleep(20 * time.Millisecond) // дать горутинам сойтись на singleflight
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
		if pairs[i].AccessToken != "fresh" {
			t.Fatalf("refresh %d: pair = %+v", i, pairs[i])
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("network exchanges = %d, want 1 (the refresh token is single-use)", hits.Load())
	}
	pair, saves := store.snapshot()
	if saves != 1 || pair.RefreshToken != "r2" {
		t.Fatalf("saves = %d, pair = %+v", saves, pair)
	}
}

// Token отдаёт сохранённый access-токен, пока до истечения больше запаса.
func TestRefreshCoordinator_TokenUsesStoredPair(t *testing.T) {
	var hits atomic.Int32
	srv := refreshServer(t, &hits, nil)

	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	c, err := New(Config{RateRPS: 1000, HostOverride: srv.URL}, testHTTPConfig(), clk, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := &memStore{pair: TokenPair{AccessToken: "live", RefreshToken: "r1", ExpiresIn: 3600, ServerTime: clk.Now().Unix()}}
	rc := NewRefreshCoordinator(c, store, testOAuthConfig(), WithRefreshSkew(time.Minute))

	tok, err := rc.Token(context.Background(), testAccount())
	if err != nil || tok != "live" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if hits.Load() != 0 {
		t.Fatalf("a live token must not be refreshed: hits = %d", hits.Load())
	}

	clk.Advance(3600 * time.Second) // токен истёк
	tok, err = rc.Token(context.Background(), testAccount())
	if err != nil || tok != "fresh" {
		t.Fatalf("Token after expiry = %q, %v", tok, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestRefreshCoordinator_Errors(t *testing.T) {
	var hits atomic.Int32
	srv := refreshServer(t, &hits, nil)
	c := newTestClient(t, srv.URL)
	ctx := context.Background()

	t.Run("no refresh token", func(t *testing.T) {
		rc := NewRefreshCoordinator(c, &memStore{}, testOAuthConfig())
		if _, err := rc.Refresh(ctx, testAccount()); !errors.Is(err, ErrAuth) {
			t.Fatalf("err = %v, want ErrAuth", err)
		}
	})

	t.Run("load error", func(t *testing.T) {
		boom := errors.New("boom")
		rc := NewRefreshCoordinator(c, &memStore{loadErr: boom}, testOAuthConfig())
		if _, err := rc.Token(ctx, testAccount()); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})

	t.Run("save error", func(t *testing.T) {
		boom := errors.New("no db")
		store := &memStore{pair: TokenPair{RefreshToken: "r1"}, saveErr: boom}
		rc := NewRefreshCoordinator(c, store, testOAuthConfig())
		if _, err := rc.Refresh(ctx, testAccount()); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want no-db", err)
		}
	})
}

// Отмена ctx одного вызывающего не отменяет общий обмен: пара всё равно сохранится.
func TestRefreshCoordinator_CallerCancelDoesNotLosePair(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := refreshServer(t, &hits, release)

	c := newTestClient(t, srv.URL)
	store := &memStore{pair: TokenPair{RefreshToken: "r1"}}
	rc := NewRefreshCoordinator(c, store, testOAuthConfig())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rc.Refresh(ctx, testAccount())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(release)

	deadline := time.After(2 * time.Second)
	for {
		if pair, saves := store.snapshot(); saves == 1 && pair.RefreshToken == "r2" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the new pair was not stored after the caller canceled")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
