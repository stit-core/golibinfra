package httpkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type IdempotencyConfig struct {
	Header string        `envconfig:"HTTP_IDEMPOTENCY_HEADER" default:"Idempotency-Key"`
	TTL    time.Duration `envconfig:"HTTP_IDEMPOTENCY_TTL" default:"24h"`
	// ReserveTTL — сколько ключ считается «выполняется», если handler не завершился.
	ReserveTTL time.Duration `envconfig:"HTTP_IDEMPOTENCY_RESERVE_TTL" default:"60s"`
	// MaxBodyBytes — потолок буферизации запроса и ответа; больше — без идемпотентности.
	MaxBodyBytes int64 `envconfig:"HTTP_IDEMPOTENCY_MAX_BODY_BYTES" default:"65536"`
}

// IdempotencyStore — байтовый store; запись сериализует httpkit.
// Reserve — атомарный резерв ключа (false, если занят), Get не отдаёт резерв
// как запись, Release снимает резерв при неудаче.
type IdempotencyStore interface {
	Reserve(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, record []byte, ttl time.Duration) error
	Release(ctx context.Context, key string) error
}

type idempotencyRecord struct {
	Fingerprint string      `json:"fp"`
	Status      int         `json:"status"`
	Header      http.Header `json:"header,omitempty"`
	Body        []byte      `json:"body,omitempty"`
}

// replayHeaders — заголовки, которые воспроизводятся из записи; остальные
// (X-Request-ID, security headers) ставит текущая цепочка middleware.
var replayHeaders = []string{"Content-Type", "Location", "ETag", "Content-Language", "Content-Disposition", "Cache-Control"}

type memoryEntry struct {
	reserved  bool
	record    []byte
	expiresAt time.Time
}

// MemoryStore — in-memory IdempotencyStore для тестов и одиночного процесса.
type MemoryStore struct {
	mu  sync.Mutex
	m   map[string]memoryEntry
	now func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: map[string]memoryEntry{}, now: time.Now}
}

func (s *MemoryStore) live(key string) (memoryEntry, bool) {
	e, ok := s.m[key]
	if !ok {
		return e, false
	}
	if !s.now().Before(e.expiresAt) {
		delete(s.m, key)
		return e, false
	}
	return e, true
}

func (s *MemoryStore) Reserve(_ context.Context, key string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.live(key); ok {
		return false, nil
	}
	s.m[key] = memoryEntry{reserved: true, expiresAt: s.now().Add(ttl)}
	return true, nil
}

func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.live(key)
	if !ok || e.reserved {
		return nil, false, nil
	}
	return e.record, true, nil
}

func (s *MemoryStore) Set(_ context.Context, key string, record []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = memoryEntry{record: record, expiresAt: s.now().Add(ttl)}
	return nil
}

func (s *MemoryStore) Release(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// idempotencyWriter буферизует ответ до лимита; переполнение = запись не хранится.
type idempotencyWriter struct {
	*responseWriter
	buf      bytes.Buffer
	limit    int64
	overflow bool
}

func (w *idempotencyWriter) Write(b []byte) (int, error) {
	if !w.overflow {
		if int64(w.buf.Len()+len(b)) > w.limit {
			w.overflow = true
			w.buf.Reset()
		} else {
			w.buf.Write(b)
		}
	}
	return w.responseWriter.Write(b)
}

// Idempotency воспроизводит ответ на повтор POST/PUT/PATCH с тем же ключом.
// Ключ scope'ится Authorization и Cookie клиента; повтор с другим телом или
// путём — 422, параллельный повтор — 409. Кэшируются только ответы < 500.
// Ошибка store — fail-open: запрос выполняется без идемпотентности.
func Idempotency(cfg IdempotencyConfig, store IdempotencyStore) func(http.Handler) http.Handler {
	instr := newInstruments()
	outcome := func(ctx context.Context, result string) {
		instr.idempotency.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
			default:
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get(cfg.Header)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()

			body, tooBig, err := bufferBody(r, cfg.MaxBodyBytes)
			if err != nil {
				var mbe *http.MaxBytesError
				if errors.As(err, &mbe) {
					writeAPIError(w, Error("BODY_TOO_LARGE", http.StatusRequestEntityTooLarge, "request body too large"))
					return
				}
				writeAPIError(w, BadRequest("BAD_BODY", "cannot read request body"))
				return
			}
			if tooBig {
				outcome(ctx, "skipped")
				next.ServeHTTP(w, r)
				return
			}
			fp := fingerprint(r, body)
			skey := scopedKey(r, key)

			if rec, found, err := loadRecord(ctx, store, skey); err != nil {
				outcome(ctx, "error")
				next.ServeHTTP(w, r)
				return
			} else if found {
				replay(w, rec, fp, outcome)
				return
			}

			ok, err := store.Reserve(ctx, skey, cfg.ReserveTTL)
			if err != nil {
				outcome(ctx, "error")
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				if rec, found, _ := loadRecord(ctx, store, skey); found {
					replay(w, rec, fp, outcome)
					return
				}
				outcome(ctx, "in_progress")
				writeAPIError(w, Error("IDEMPOTENCY_IN_PROGRESS", http.StatusConflict,
					"a request with this Idempotency-Key is still being processed"))
				return
			}

			ww := &idempotencyWriter{responseWriter: wrap(w), limit: cfg.MaxBodyBytes}
			next.ServeHTTP(ww, r)

			if ww.status >= http.StatusInternalServerError || ww.overflow {
				_ = store.Release(ctx, skey)
				outcome(ctx, "skipped")
				return
			}
			rec := idempotencyRecord{Fingerprint: fp, Status: ww.status, Header: http.Header{}, Body: ww.buf.Bytes()}
			for _, h := range replayHeaders {
				if v := ww.Header().Values(h); len(v) > 0 {
					rec.Header[h] = v
				}
			}
			raw, err := json.Marshal(rec)
			if err == nil {
				err = store.Set(ctx, skey, raw, cfg.TTL)
			}
			if err != nil {
				_ = store.Release(ctx, skey)
				outcome(ctx, "error")
				return
			}
			outcome(ctx, "stored")
		})
	}
}

func replay(w http.ResponseWriter, rec idempotencyRecord, fp string, outcome func(context.Context, string)) {
	ctx := context.Background()
	if rec.Fingerprint != fp {
		outcome(ctx, "conflict")
		writeAPIError(w, Error("IDEMPOTENCY_KEY_REUSED", http.StatusUnprocessableEntity,
			"Idempotency-Key was already used with a different request"))
		return
	}
	for k, v := range rec.Header {
		w.Header()[k] = v
	}
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(rec.Body)
	outcome(ctx, "replayed")
}

func loadRecord(ctx context.Context, store IdempotencyStore, key string) (idempotencyRecord, bool, error) {
	var rec idempotencyRecord
	raw, found, err := store.Get(ctx, key)
	if err != nil || !found {
		return rec, false, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, false, err
	}
	return rec, true, nil
}

// bufferBody читает тело до limit; при переполнении тело восстанавливается
// для сквозного выполнения, а идемпотентность для запроса пропускается.
func bufferBody(r *http.Request, limit int64) ([]byte, bool, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, false, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > limit {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
		return nil, true, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, false, nil
}

func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// scopedKey привязывает ключ к клиенту: чужой ключ не отдаёт чужой ответ.
func scopedKey(r *http.Request, key string) string {
	h := sha256.New()
	h.Write([]byte(r.Header.Get("Authorization")))
	h.Write([]byte{0})
	h.Write([]byte(r.Header.Get("Cookie")))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))
}
