package jwks_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/jwks"
)

func jwk(t *testing.T, kid string) jose.JSONWebKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return jose.JSONWebKey{Key: priv.Public(), KeyID: kid, Algorithm: string(jose.ES256), Use: "sig"}
}

// keyServer отдаёт текущий набор ключей и считает загрузки.
type keyServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	keys   []jose.JSONWebKey
	status int
	hits   atomic.Int64
}

func newKeyServer(t *testing.T, keys ...jose.JSONWebKey) *keyServer {
	t.Helper()
	ks := &keyServer{keys: keys, status: http.StatusOK}
	ks.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ks.hits.Add(1)
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if ks.status != http.StatusOK {
			w.WriteHeader(ks.status)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: ks.keys})
	}))
	t.Cleanup(ks.srv.Close)
	return ks
}

func (ks *keyServer) set(status int, keys ...jose.JSONWebKey) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.status = status
	ks.keys = keys
}

func TestKey_UnknownKIDStorm_SingleFetchPerInterval(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{Clock: clock})
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if _, err := c.Key(ctx, "attacker-kid"); !errors.Is(err, jwks.ErrUnknownKey) {
				t.Errorf("ожидали ErrUnknownKey, получили %v", err)
			}
		})
	}
	wg.Wait()
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("100 промахов по kid → ожидали 1 загрузку, получили %d", got)
	}

	// Известный kid из кэша — без сети.
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("известный kid вызвал загрузку: %d", got)
	}

	// По истечении интервала промах снова ведёт к загрузке — ровно одной.
	clock.Advance(jwks.DefaultMinRefresh)
	for range 10 {
		_, _ = c.Key(ctx, "attacker-kid")
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("после интервала ожидали 2 загрузки, получили %d", got)
	}
}

func TestKey_RotationPicksNewKID(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{Clock: clock})
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key k1: %v", err)
	}
	ks.set(http.StatusOK, jwk(t, "k1"), jwk(t, "k2"))

	// Внутри интервала новый kid ещё неизвестен (троттлинг), после — подтянут.
	if _, err := c.Key(ctx, "k2"); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("до интервала ожидали ErrUnknownKey, получили %v", err)
	}
	clock.Advance(jwks.DefaultMinRefresh)
	k, err := c.Key(ctx, "k2")
	if err != nil {
		t.Fatalf("Key k2 после ротации: %v", err)
	}
	if k.KeyID != "k2" {
		t.Fatalf("kid=%s", k.KeyID)
	}
}

func TestRefresh_ForceIgnoresThrottle(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{})
	ctx := context.Background()

	for range 3 {
		if err := c.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if got := ks.hits.Load(); got != 3 {
		t.Fatalf("Refresh должен грузить всегда: %d загрузок", got)
	}
}

func TestKey_FailureIsThrottledToo(t *testing.T) {
	ks := newKeyServer(t)
	ks.set(http.StatusInternalServerError)
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{Clock: clock})
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err == nil {
		t.Fatal("недоступный JWKS не дал ошибку")
	}
	// Пока набор не загружен, повторные вызовы отдают ошибку последней попытки без сети.
	if _, err := c.Key(ctx, "k1"); err == nil || errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("ожидали ошибку загрузки, получили %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("провал не затроттлен: %d загрузок", got)
	}

	ks.set(http.StatusOK, jwk(t, "k1"))
	clock.Advance(jwks.DefaultMinRefresh)
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("после восстановления: %v", err)
	}
}

func TestKey_StaleKeysSurviveOutage(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	clock := clockkit.NewTickable(time.Now())
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{Clock: clock})
	ctx := context.Background()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("Key: %v", err)
	}
	ks.set(http.StatusBadGateway)
	clock.Advance(jwks.DefaultMinRefresh)

	if _, err := c.Key(ctx, "k2"); err == nil {
		t.Fatal("промах при недоступном сервере не дал ошибку")
	}
	// Загруженные ключи остаются рабочими при недоступном issuer'е.
	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("старые ключи потеряны: %v", err)
	}
}

func TestKey_NoKeys(t *testing.T) {
	ks := newKeyServer(t)
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{})
	if err := c.Refresh(context.Background()); !errors.Is(err, jwks.ErrNoKeys) {
		t.Fatalf("ожидали ErrNoKeys, получили %v", err)
	}
}

func TestKey_EmptyKID(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "only"))
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{})
	ctx := context.Background()

	k, err := c.Key(ctx, "")
	if err != nil || k.KeyID != "only" {
		t.Fatalf("единственный ключ без kid: k=%s err=%v", k.KeyID, err)
	}

	ks.set(http.StatusOK, jwk(t, "a"), jwk(t, "b"))
	if err := c.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := c.Key(ctx, ""); !errors.Is(err, jwks.ErrUnknownKey) {
		t.Fatalf("пустой kid при нескольких ключах: ожидали ErrUnknownKey, получили %v", err)
	}
}

func TestFetch_DetachedFromCallerCancel(t *testing.T) {
	ks := newKeyServer(t, jwk(t, "k1"))
	var seen atomic.Int64
	c := jwks.New(ks.srv.URL, ks.srv.Client(), jwks.Options{
		OnRefresh: func(_ context.Context, err error) {
			if err == nil {
				seen.Add(1)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Key(ctx, "k1"); err != nil {
		t.Fatalf("отменённый ctx вызывающего уронил общую загрузку: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("OnRefresh не вызван: %d", seen.Load())
	}
}

func TestNew_NilDoerHasTimeout(t *testing.T) {
	// Сервер молчит дольше таймаута — nil Doer обязан оборваться сам.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := jwks.New(srv.URL, nil, jwks.Options{Timeout: 50 * time.Millisecond})
	start := time.Now()
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("ожидали ошибку таймаута")
	}
	if time.Since(start) > time.Second {
		t.Fatal("таймаут не сработал")
	}
}
