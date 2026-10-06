package httpkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func idemCfg() IdempotencyConfig {
	return IdempotencyConfig{
		Header: "Idempotency-Key", TTL: time.Minute, ReserveTTL: time.Minute,
		MaxBodyBytes: 64, StoreTimeout: 2 * time.Second,
	}
}

func postWithKey(key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Authorization", "Bearer alice")
	return req
}

func createdHandler(calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/orders/1")
		w.Header().Set("X-Custom", "not-replayed")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
}

func TestIdempotency_ReplaysSameRequest(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, postWithKey("k1", `{"a":1}`))
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postWithKey("k1", `{"a":1}`))

	if calls.Load() != 1 {
		t.Fatalf("handler calls: %d", calls.Load())
	}
	if second.Code != http.StatusCreated || second.Body.String() != `{"id":1}` {
		t.Fatalf("replay: %d %q", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay must be marked")
	}
	if second.Header().Get("Location") != "/orders/1" || second.Header().Get("Content-Type") != "application/json" {
		t.Errorf("replay headers: %v", second.Header())
	}
	if second.Header().Get("X-Custom") != "" {
		t.Error("non-whitelisted header must not be replayed")
	}
}

func TestIdempotency_DifferentBodySameKey_Conflict(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{"a":1}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{"a":2}`))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler must not run for mismatched fingerprint, calls=%d", calls.Load())
	}
}

func TestIdempotency_KeyScopedToClient(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{"a":1}`))
	other := postWithKey("k1", `{"a":1}`)
	other.Header.Set("Authorization", "Bearer bob")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, other)

	if calls.Load() != 2 {
		t.Fatalf("another client with the same key must execute its own request, calls=%d", calls.Load())
	}
	if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Error("must not replay another client's response")
	}
}

func TestIdempotency_GetSkipped(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))
	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		req.Header.Set("Idempotency-Key", "k-get")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls.Load() != 3 {
		t.Fatalf("GET must bypass idempotency, calls=%d", calls.Load())
	}
}

func TestIdempotency_5xxNotCached(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	if calls.Load() != 2 {
		t.Fatalf("5xx must not be replayed, calls=%d", calls.Load())
	}
}

func TestIdempotency_OversizedBodySkipped(t *testing.T) {
	var (
		calls atomic.Int32
		seen  int
	)
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		seen = len(b)
		w.WriteHeader(http.StatusCreated)
	}))
	big := strings.Repeat("x", 200)
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", big))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", big))
	if calls.Load() != 2 {
		t.Fatalf("oversized body must pass through without idempotency, calls=%d", calls.Load())
	}
	if seen != 200 {
		t.Fatalf("handler must see the whole body, got %d bytes", seen)
	}
}

type stubStore struct {
	reserve bool
	err     error
}

func (s stubStore) Reserve(context.Context, string, time.Duration) (string, bool, error) {
	return "t", s.reserve, s.err
}
func (s stubStore) Get(context.Context, string) ([]byte, bool, error) { return nil, false, s.err }
func (s stubStore) Set(context.Context, string, string, []byte, time.Duration) (bool, error) {
	return s.err == nil, s.err
}
func (s stubStore) Release(context.Context, string, string) error { return nil }

func TestIdempotency_InProgress(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), stubStore{reserve: false}, logkit.Nop())(createdHandler(&calls))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 while in progress, got %d", rec.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("handler must not run while the key is reserved")
	}
}

func TestIdempotency_StoreErrorFailsOpen(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), stubStore{err: errors.New("redis down")}, logkit.Nop())(createdHandler(&calls))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("store error must fail open: %d calls=%d", rec.Code, calls.Load())
	}
}

// N8 валидатора: отказ store виден в логе, а не только в метрике — и fail-open,
// и после ответа хендлера (Set).
func TestIdempotency_StoreErrorIsLogged(t *testing.T) {
	stores := map[string]IdempotencyStore{
		"get": stubStore{err: errors.New("redis down")},
		"set": &setErrorStore{MemoryStore: NewMemoryStore()},
	}
	for op, store := range stores {
		t.Run(op, func(t *testing.T) {
			var buf bytes.Buffer
			log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			Idempotency(idemCfg(), store, log)(createdHandler(&calls)).ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))

			out := buf.String()
			if !strings.Contains(out, `"msg":"idempotency store failed"`) || !strings.Contains(out, `"op":"`+op+`"`) {
				t.Fatalf("store failure must be logged with op %s: %s", op, out)
			}
		})
	}
}

func TestMemoryStore_Expiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	if _, ok, _ := s.Reserve(ctx, "k", time.Minute); !ok {
		t.Fatal("first reserve")
	}
	if _, ok, _ := s.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("reserve while reserved")
	}
	now = now.Add(2 * time.Minute)
	token, ok, _ := s.Reserve(ctx, "k", time.Minute)
	if !ok {
		t.Fatal("reserve after expiry")
	}
	if stored, _ := s.Set(ctx, "k", token, []byte("rec"), time.Minute); !stored {
		t.Fatal("set by the owner")
	}
	if v, found, _ := s.Get(ctx, "k"); !found || string(v) != "rec" {
		t.Fatalf("get: %q %v", v, found)
	}
	now = now.Add(2 * time.Minute)
	if _, found, _ := s.Get(ctx, "k"); found {
		t.Fatal("record must expire")
	}
}

// Регрессия KSEC-06: новый ключ сверх предела — ошибка Reserve (fail-open или 503 по FailClosed),
// живые ключи не вытесняются, истёкшие освобождают место после прохода.
func TestMemoryStore_MaxKeys(t *testing.T) {
	if got := NewMemoryStore().maxKeys; got != 100_000 {
		t.Fatalf("default maxKeys = %d, want the documented 100 000", got)
	}
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore()
	s.now = func() time.Time { return now }
	s.maxKeys = 2
	ctx := context.Background()
	for _, k := range []string{"a", "b"} {
		if _, ok, err := s.Reserve(ctx, k, time.Minute); !ok || err != nil {
			t.Fatalf("reserve %q: ok=%v err=%v", k, ok, err)
		}
	}
	if _, ok, err := s.Reserve(ctx, "c", time.Minute); ok || !errors.Is(err, errMemoryStoreFull) {
		t.Fatalf("reserve over the limit: ok=%v err=%v, want errMemoryStoreFull", ok, err)
	}
	if _, ok, err := s.Reserve(ctx, "a", time.Minute); ok || err != nil {
		t.Fatalf("live key must stay reserved: ok=%v err=%v", ok, err)
	}
	now = now.Add(time.Minute + memorySweepEvery)
	if _, ok, err := s.Reserve(ctx, "c", time.Minute); !ok || err != nil {
		t.Fatalf("reserve after keys expired: ok=%v err=%v", ok, err)
	}
}

// ctxProbeStore фиксирует состояние ctx на каждой операции store.
type ctxProbeStore struct {
	inner   *MemoryStore
	mu      sync.Mutex
	ctxErrs map[string]error
	calls   map[string]int
}

func newCtxProbeStore() *ctxProbeStore {
	return &ctxProbeStore{inner: NewMemoryStore(), ctxErrs: map[string]error{}, calls: map[string]int{}}
}

func (s *ctxProbeStore) note(op string, ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctxErrs[op] = ctx.Err()
	s.calls[op]++
}

func (s *ctxProbeStore) Reserve(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	s.note("reserve", ctx)
	return s.inner.Reserve(ctx, key, ttl)
}

func (s *ctxProbeStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.note("get", ctx)
	return s.inner.Get(ctx, key)
}

func (s *ctxProbeStore) Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (bool, error) {
	s.note("set", ctx)
	return s.inner.Set(ctx, key, token, record, ttl)
}

func (s *ctxProbeStore) Release(ctx context.Context, key, token string) error {
	s.note("release", ctx)
	return s.inner.Release(ctx, key, token)
}

// Регрессия: клиент отвалился по таймауту — результат обязан сохраниться,
// иначе его же ретрай выполнит операцию второй раз.
func TestIdempotency_StoresResultAfterClientDisconnect(t *testing.T) {
	store := newCtxProbeStore()
	var calls atomic.Int32
	h := Idempotency(idemCfg(), store, logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cancel, _ := r.Context().Value(cancelKey{}).(context.CancelFunc)
		cancel()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	req := postWithKey("k1", `{"a":1}`).WithContext(context.WithValue(ctx, cancelKey{}, cancel))
	h.ServeHTTP(httptest.NewRecorder(), req)
	cancel()

	if err := store.ctxErrs["set"]; err != nil {
		t.Fatalf("Set must run on a live ctx, got %v", err)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postWithKey("k1", `{"a":1}`))
	if calls.Load() != 1 || second.Header().Get(HeaderIdempotentReplayed) != "true" {
		t.Fatalf("retry must replay: calls=%d replayed=%q", calls.Load(), second.Header().Get(HeaderIdempotentReplayed))
	}
}

type cancelKey struct{}

// Регрессия: query — часть идентичности запроса; ?dry_run=true не должен
// получать ответ реального вызова.
func TestIdempotency_QueryIsPartOfFingerprint(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))

	dry := httptest.NewRequest(http.MethodPost, "/orders?dry_run=true", strings.NewReader(`{"a":1}`))
	dry.Header.Set("Idempotency-Key", "k1")
	dry.Header.Set("Authorization", "Bearer alice")
	h.ServeHTTP(httptest.NewRecorder(), dry)

	real := httptest.NewRecorder()
	h.ServeHTTP(real, postWithKey("k1", `{"a":1}`))

	if real.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key with a different query must be rejected, got %d", real.Code)
	}
	if real.Header().Get(HeaderIdempotentReplayed) != "" {
		t.Fatal("dry-run response must not be replayed as the real one")
	}
}

func TestIdempotency_QueryOrderDoesNotMatter(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))
	for _, target := range []string{"/orders?b=2&a=1", "/orders?a=1&b=2"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "k1")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls.Load() != 1 {
		t.Fatalf("query order must not change the fingerprint, calls=%d", calls.Load())
	}
}

// Регрессия: panic снимает резерв, иначе ретраи получают 409 до конца ReserveTTL.
func TestIdempotency_PanicReleasesReservation(t *testing.T) {
	var calls atomic.Int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		w.WriteHeader(http.StatusCreated)
	})
	h := Recovery(logkit.Nop())(Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(inner))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, postWithKey("k1", `{}`))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("panic must become 500, got %d", first.Code)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postWithKey("k1", `{}`))
	if second.Code != http.StatusCreated {
		t.Fatalf("retry after panic must run the handler, got %d", second.Code)
	}
}

func TestIdempotency_FailClosed(t *testing.T) {
	cfg := idemCfg()
	cfg.FailClosed = true
	var calls atomic.Int32
	h := Idempotency(cfg, stubStore{err: errors.New("redis down")}, logkit.Nop())(createdHandler(&calls))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail-closed store error must answer 503, got %d", rec.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("handler must not run when the store is unavailable and fail-closed")
	}
}

type corruptStore struct{ *MemoryStore }

func (s corruptStore) Get(context.Context, string) ([]byte, bool, error) {
	return []byte("{not json"), true, nil
}

func TestIdempotency_CorruptRecordFailsOpen(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), corruptStore{NewMemoryStore()}, logkit.Nop())(createdHandler(&calls))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postWithKey("k1", `{}`))
	if rec.Code != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("corrupt record must fail open: %d calls=%d", rec.Code, calls.Load())
	}
}

type setErrorStore struct {
	*MemoryStore
	released atomic.Int32
}

func (s *setErrorStore) Set(context.Context, string, string, []byte, time.Duration) (bool, error) {
	return false, errors.New("set failed")
}

func (s *setErrorStore) Release(ctx context.Context, key, token string) error {
	s.released.Add(1)
	return s.MemoryStore.Release(ctx, key, token)
}

func TestIdempotency_SetErrorReleasesReservation(t *testing.T) {
	store := &setErrorStore{MemoryStore: NewMemoryStore()}
	var calls atomic.Int32
	h := Idempotency(idemCfg(), store, logkit.Nop())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	if store.released.Load() != 1 {
		t.Fatalf("failed Set must release the reservation, releases=%d", store.released.Load())
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, postWithKey("k1", `{}`))
	if second.Code != http.StatusCreated || calls.Load() != 2 {
		t.Fatalf("retry must proceed: %d calls=%d", second.Code, calls.Load())
	}
}

// Регрессия KCOR-05 (E07): ответ запроса, пережившего ReserveTTL, не затирает резерв повтора,
// который уже занял ключ, — запись не ставится, в логе WARN, исход skipped.
func TestIdempotency_ExpiredReservationIsNotOverwritten(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	cfg := idemCfg()
	var skey, retry string
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now = now.Add(cfg.ReserveTTL + time.Second)
		skey = scopedKey(r, "k1")
		var ok bool
		if retry, ok, _ = store.Reserve(r.Context(), skey, time.Minute); !ok {
			t.Error("retry could not reserve the expired key")
		}
		w.WriteHeader(http.StatusCreated)
	})
	before := metrictest.Collect(t)
	Idempotency(cfg, store, log)(slow).ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))

	if out := buf.String(); !strings.Contains(out, `"msg":"idempotency reservation expired before the response"`) ||
		!strings.Contains(out, `"reserve_ttl":"1m0s"`) {
		t.Errorf("expired reservation must be logged with reserve_ttl: %s", out)
	}
	d := metrictest.Collect(t).Sub(before)
	if n := d.Value("http_idempotency_total", "result", "skipped"); n != 1 {
		t.Errorf("http_idempotency_total{result=skipped}: %v, want 1", n)
	}
	if n := d.Value("http_idempotency_total", "result", "stored"); n != 0 {
		t.Errorf("http_idempotency_total{result=stored}: %v, want 0", n)
	}
	if rec, found, _ := store.Get(context.Background(), skey); found {
		t.Fatalf("late response overwrote the retry's reservation: %s", rec)
	}
	if stored, _ := store.Set(context.Background(), skey, retry, []byte("rec"), time.Minute); !stored {
		t.Fatal("the retry lost its reservation")
	}
}

func TestIdempotency_OversizedResponseNotStored(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(strings.Repeat("x", 200)))
	}))
	first := httptest.NewRecorder()
	h.ServeHTTP(first, postWithKey("k1", `{}`))
	if first.Body.Len() != 200 {
		t.Fatalf("client must still get the whole response, got %d bytes", first.Body.Len())
	}
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	if calls.Load() != 2 {
		t.Fatalf("oversized response must not be replayed, calls=%d", calls.Load())
	}
}

// Регрессия: replay до аутентификации отдал бы сохранённый ответ отозванной
// сессии; ключ scope'ится субъектом, а не сырым заголовком.
func TestIdempotency_WithAuth(t *testing.T) {
	var calls atomic.Int32
	extract := func(r *http.Request) (context.Context, error) {
		token := r.Header.Get("Authorization")
		if token == "Bearer revoked" {
			return nil, errors.New("session revoked")
		}
		return ContextWithSubject(r.Context(), "user-1"), nil
	}
	store := NewMemoryStore()
	h := Idempotency(idemCfg(), store, logkit.Nop(), WithAuth(extract))(createdHandler(&calls))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, postWithKey("k1", `{"a":1}`))
	if first.Code != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("first call: %d calls=%d", first.Code, calls.Load())
	}

	revoked := postWithKey("k1", `{"a":1}`)
	revoked.Header.Set("Authorization", "Bearer revoked")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, revoked)
	if rec.Header().Get(HeaderIdempotentReplayed) != "" {
		t.Fatal("revoked credentials must not receive a replay")
	}
	if calls.Load() != 2 {
		t.Fatalf("unauthenticated request goes to the handler (auth layer answers): calls=%d", calls.Load())
	}

	// Тот же субъект после ротации токена — тот же scope, значит replay.
	rotated := postWithKey("k1", `{"a":1}`)
	rotated.Header.Set("Authorization", "Bearer new-token")
	replayed := httptest.NewRecorder()
	h.ServeHTTP(replayed, rotated)
	if replayed.Header().Get(HeaderIdempotentReplayed) != "true" || calls.Load() != 2 {
		t.Fatalf("token rotation must not duplicate the operation: replayed=%q calls=%d",
			replayed.Header().Get(HeaderIdempotentReplayed), calls.Load())
	}
}

func TestIdempotency_WithAuthMarksContextAuthenticated(t *testing.T) {
	extract := func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	var seen bool
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop(), WithAuth(extract))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = authDone(r.Context())
		w.WriteHeader(http.StatusCreated)
	}))
	h.ServeHTTP(httptest.NewRecorder(), postWithKey("k1", `{}`))
	if !seen {
		t.Fatal("HumaAuth must not re-verify credentials already checked by Idempotency")
	}
}

// Регрессия (NF4): резерв, переживший ReserveTTL, своим отложенным Release не
// снимает резерв следующего запроса — иначе третий запрос идёт параллельно второму.
func TestMemoryStore_ReleaseIsScopedToOwner(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	stale, ok, _ := s.Reserve(ctx, "k", time.Minute)
	if !ok {
		t.Fatal("first reserve")
	}
	now = now.Add(2 * time.Minute)
	fresh, ok, _ := s.Reserve(ctx, "k", time.Minute)
	if !ok || fresh == stale {
		t.Fatalf("reserve after expiry: ok=%v token=%q", ok, fresh)
	}
	if err := s.Release(ctx, "k", stale); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("a late Release must not drop the reservation of another request")
	}
	if err := s.Release(ctx, "k", fresh); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Reserve(ctx, "k", time.Minute); !ok {
		t.Fatal("the owner must be able to release its own reservation")
	}
}

// Регрессия: ключ идемпотентности уникален, к истёкшей записи не обращаются —
// без sweep'а map растёт на каждый write-запрос.
func TestMemoryStore_SweepsExpiredEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore()
	s.now = func() time.Time { return now }
	ctx := context.Background()

	for i := range 100 {
		if _, ok, _ := s.Reserve(ctx, "k"+strconv.Itoa(i), time.Minute); !ok {
			t.Fatalf("reserve %d", i)
		}
	}
	s.mu.Lock()
	size := len(s.m)
	s.mu.Unlock()
	if size != 100 {
		t.Fatalf("size before expiry: %d", size)
	}

	now = now.Add(2*time.Minute + memorySweepEvery)
	if _, ok, _ := s.Reserve(ctx, "fresh", time.Minute); !ok {
		t.Fatal("reserve fresh")
	}
	s.mu.Lock()
	size = len(s.m)
	s.mu.Unlock()
	if size != 1 {
		t.Fatalf("expired entries must be swept, size=%d", size)
	}
}
