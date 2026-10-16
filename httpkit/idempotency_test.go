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
	return postAs("alice", "/orders", key, body)
}

// postAs — запрос субъекта subject: так его оставляет extractor схемы (ContextWithSubject).
func postAs(subject, target, key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	return req.WithContext(ContextWithSubject(req.Context(), subject))
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

// Регрессия RTST-01: scope ключа — субъект. Другой субъект с тем же ключом и телом исполняет свой
// запрос, а не получает сохранённый ответ первого; повтор первого — replay.
func TestIdempotency_KeyScopedToSubject(t *testing.T) {
	var calls atomic.Int32
	h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(createdHandler(&calls))

	h.ServeHTTP(httptest.NewRecorder(), postAs("alice", "/orders", "k1", `{"a":1}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postAs("bob", "/orders", "k1", `{"a":1}`))

	if calls.Load() != 2 {
		t.Fatalf("another subject with the same key must execute its own request, calls=%d", calls.Load())
	}
	if rec.Header().Get(HeaderIdempotentReplayed) != "" {
		t.Error("must not replay another subject's response")
	}
	again := httptest.NewRecorder()
	h.ServeHTTP(again, postAs("alice", "/orders", "k1", `{"a":1}`))
	if again.Header().Get(HeaderIdempotentReplayed) != "true" || calls.Load() != 2 {
		t.Fatalf("the first subject's retry: replayed=%q calls=%d; want a replay", again.Header().Get(HeaderIdempotentReplayed), calls.Load())
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

// failingBody отдаёт prefix, затем ошибку чтения: тело, которое Idempotency не дочитала в буфер.
type failingBody struct {
	prefix *strings.Reader
	err    error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	return 0, b.err
}

func (b *failingBody) Close() error { return nil }

// VGK-03: тело, которое не легло в буфер (сверх лимита тела, ошибка чтения), идёт без идемпотентности — handler читает
// его сам и отвечает, как без ключа, а не 413 или 400 Idempotency.
func TestIdempotency_UnbufferedBodyGoesToHandler(t *testing.T) {
	errDropped := errors.New("connection reset")
	for name, body := range map[string]func(w http.ResponseWriter) io.ReadCloser{
		"over the body limit": func(w http.ResponseWriter) io.ReadCloser {
			return http.MaxBytesReader(w, io.NopCloser(strings.NewReader(strings.Repeat("x", 40))), 16)
		},
		"read error": func(http.ResponseWriter) io.ReadCloser {
			return &failingBody{prefix: strings.NewReader(strings.Repeat("x", 16)), err: errDropped}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var seen []byte
			var readErr error
			h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				seen, readErr = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusAccepted)
			}))
			rec := httptest.NewRecorder()
			req := postWithKey("k1", "")
			req.Body = body(rec)
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted || calls.Load() != 1 {
				t.Fatalf("status %d, handler calls %d: the handler answers a body that is not buffered", rec.Code, calls.Load())
			}
			if string(seen) != strings.Repeat("x", 16) || readErr == nil {
				t.Fatalf("the handler reads %q, %v: the bytes read by Idempotency and then the read error", seen, readErr)
			}
			if name == "over the body limit" && !IsMaxBodyError(readErr) {
				t.Fatalf("the handler must get *http.MaxBytesError, got %v", readErr)
			}
		})
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

	req := postWithKey("k1", `{"a":1}`)
	ctx, cancel := context.WithCancel(req.Context())
	h.ServeHTTP(httptest.NewRecorder(), req.WithContext(context.WithValue(ctx, cancelKey{}, cancel)))
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

	h.ServeHTTP(httptest.NewRecorder(), postAs("alice", "/orders?dry_run=true", "k1", `{"a":1}`))

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
		h.ServeHTTP(httptest.NewRecorder(), postAs("alice", target, "k1", `{}`))
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
		skey = scopedKey(SubjectFrom(r.Context()), "k1")
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

// Регрессия KCOR-10 (H16): ответ «не выполнено, повторите» не сохраняется — повтор с тем же
// ключом исполняет запрос заново, а не получает отказ на сутки.
func TestIdempotency_NotExecutedResponsesAreNotStored(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			h := Idempotency(idemCfg(), NewMemoryStore(), logkit.Nop())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusCreated)
			}))
			first := httptest.NewRecorder()
			h.ServeHTTP(first, postWithKey("k1", `{}`))
			second := httptest.NewRecorder()
			h.ServeHTTP(second, postWithKey("k1", `{}`))
			if first.Code != status || second.Code != http.StatusCreated || calls.Load() != 2 {
				t.Fatalf("first %d, retry %d, calls %d; want %d, then a fresh 201", first.Code, second.Code, calls.Load(), status)
			}
		})
	}
}

// Регрессия KSEC-14 (H16) и RSEC-01: запрос без субъекта идёт без дедупликации и в store не
// пишет — с любыми Authorization и Cookie: credentials запроса — не личность, клиент менял бы
// их вместе со scope, а записи таких запросов заняли бы store.
func TestIdempotency_RequestsWithoutIdentityAreNotDeduplicated(t *testing.T) {
	for name, header := range map[string][2]string{
		"no credentials":        {},
		"unverified cookie":     {"Cookie", "lang=ru"},
		"unverified authorized": {"Authorization", "Bearer anything"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			store := NewMemoryStore()
			h := Idempotency(idemCfg(), store, logkit.Nop())(createdHandler(&calls))
			before := metrictest.Collect(t)
			for _, remote := range []string{"198.51.100.1:1000", "203.0.113.2:2000"} {
				req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(`{}`))
				req.RemoteAddr = remote
				req.Header.Set("Idempotency-Key", "1")
				if header[0] != "" {
					req.Header.Set(header[0], header[1])
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Header().Get(HeaderIdempotentReplayed) != "" {
					t.Fatalf("client %s without identity got a replayed response", remote)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("handler calls = %d, want 2", calls.Load())
			}
			if len(store.m) != 0 {
				t.Fatalf("store holds %d keys of clients without identity, want 0", len(store.m))
			}
			if n := metrictest.Collect(t).Sub(before).Value("http_idempotency_total", "result", "skipped"); n != 2 {
				t.Fatalf("http_idempotency_total{result=skipped} = %v, want 2", n)
			}
		})
	}
}

// Регрессия RSEC-01: один scope сохраняет не больше бюджета ответов за окно TTL. Новый ключ сверх
// него — как отказ store этому клиенту (fail-open: без дедупликации, с записью лога; FailClosed —
// 503), сохранённые ответы воспроизводятся, чужой scope не задет; окно истекло — бюджет новый.
func TestIdempotency_ScopeKeyBudget(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		t.Run("fail_closed="+strconv.FormatBool(failClosed), func(t *testing.T) {
			var calls atomic.Int32
			var buf bytes.Buffer
			log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
			if err != nil {
				t.Fatal(err)
			}
			cfg := idemCfg()
			cfg.FailClosed = failClosed
			store := NewMemoryStore()
			now := time.Unix(1_700_000_000, 0)
			budget := newKeyBudget(cfg.TTL)
			budget.limit, budget.now = 2, func() time.Time { return now }
			h := idempotency(cfg, store, log, budget)(createdHandler(&calls))
			send := func(subject, key string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, postAs(subject, "/orders", key, `{}`))
				return rec
			}
			send("alice", "k1")
			send("alice", "k2")
			before := metrictest.Collect(t)

			over := send("alice", "k3")
			again := send("alice", "k3")
			switch {
			case failClosed && (over.Code != http.StatusServiceUnavailable || calls.Load() != 2):
				t.Fatalf("over budget, fail-closed: %d, calls=%d; want 503 without execution", over.Code, calls.Load())
			case !failClosed && (over.Code != http.StatusCreated || again.Header().Get(HeaderIdempotentReplayed) != "" || calls.Load() != 4):
				t.Fatalf("over budget, fail-open: %d replayed=%q calls=%d; want two executions without a replay",
					over.Code, again.Header().Get(HeaderIdempotentReplayed), calls.Load())
			}
			if len(store.m) != 2 {
				t.Fatalf("store holds %d keys, want the 2 of the budget", len(store.m))
			}
			if out := buf.String(); !strings.Contains(out, `"msg":"idempotency store failed"`) || !strings.Contains(out, "key budget of the client is exhausted") {
				t.Errorf("an exhausted budget must be logged as a store refusal: %s", out)
			}
			if n := metrictest.Collect(t).Sub(before).Value("http_idempotency_total", "result", "error"); n != 2 {
				t.Errorf("http_idempotency_total{result=error} = %v, want 2", n)
			}

			executed := calls.Load()
			if rec := send("alice", "k1"); rec.Header().Get(HeaderIdempotentReplayed) != "true" || calls.Load() != executed {
				t.Fatalf("a stored response must still be replayed over budget: replayed=%q", rec.Header().Get(HeaderIdempotentReplayed))
			}
			send("bob", "k1")
			if rec := send("bob", "k1"); rec.Header().Get(HeaderIdempotentReplayed) != "true" || calls.Load() != executed+1 {
				t.Fatalf("another subject must keep its own budget: replayed=%q calls=%d", rec.Header().Get(HeaderIdempotentReplayed), calls.Load())
			}

			now = now.Add(cfg.TTL)
			send("alice", "k4")
			if rec := send("alice", "k4"); rec.Header().Get(HeaderIdempotentReplayed) != "true" {
				t.Fatalf("a new window must give a new budget: replayed=%q", rec.Header().Get(HeaderIdempotentReplayed))
			}
			// Новое окно — новый бюджет, а не его отсутствие: третий ключ окна снова сверх него.
			send("alice", "k5")
			send("alice", "k6")
			if rec := send("alice", "k6"); rec.Header().Get(HeaderIdempotentReplayed) != "" {
				t.Fatalf("the budget must hold in the next window too: the key over it was stored and replayed")
			}
		})
	}
}

// Таблица бюджетов ограничена: истёкшие окна выметаются, scope сверх предела таблицы не
// учитывается — память процесса не растёт с числом клиентов.
func TestKeyBudget_TableIsBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := newKeyBudget(time.Hour)
	b.limit, b.maxScopes, b.now = 1, 2, func() time.Time { return now }
	b.add("a")
	b.add("b")
	b.add("c")
	if len(b.scopes) != 2 || b.exhausted("c") || !b.exhausted("a") {
		t.Fatalf("table: %d scopes, c exhausted=%v, a exhausted=%v; want 2, false, true", len(b.scopes), b.exhausted("c"), b.exhausted("a"))
	}
	now = now.Add(time.Hour)
	b.add("c")
	if len(b.scopes) != 1 || !b.exhausted("c") || b.exhausted("a") {
		t.Fatalf("after the window: %d scopes, c exhausted=%v, a exhausted=%v; want 1, true, false", len(b.scopes), b.exhausted("c"), b.exhausted("a"))
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

// Регрессия KTST-11: резерв ключа достаётся ровно одному из параллельных запросов — остальные
// получают 409, а не исполняют хендлер второй раз.
func TestMemoryStore_ConcurrentReserve(t *testing.T) {
	s := NewMemoryStore()
	const requests = 64
	var won atomic.Int64
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			if _, ok, err := s.Reserve(context.Background(), "k", time.Minute); err == nil && ok {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if got := won.Load(); got != 1 {
		t.Fatalf("%d of %d parallel requests reserved the key, want exactly one", got, requests)
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
