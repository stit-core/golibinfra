package httpkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// IdempotencyConfig — параметры дедупликации write-запросов по ключу клиента.
type IdempotencyConfig struct {
	Header string        `envconfig:"HTTP_IDEMPOTENCY_HEADER" default:"Idempotency-Key"`
	TTL    time.Duration `envconfig:"HTTP_IDEMPOTENCY_TTL" default:"24h"`
	// ReserveTTL — сколько ключ считается «выполняется», если handler не завершился.
	ReserveTTL time.Duration `envconfig:"HTTP_IDEMPOTENCY_RESERVE_TTL" default:"60s"`
	// MaxBodyBytes — потолок буферизации запроса и ответа; больше — без идемпотентности.
	MaxBodyBytes int64 `envconfig:"HTTP_IDEMPOTENCY_MAX_BODY_BYTES" default:"65536"`
	// StoreTimeout — потолок одной операции store; ctx запроса на них не влияет.
	StoreTimeout time.Duration `envconfig:"HTTP_IDEMPOTENCY_STORE_TIMEOUT" default:"2s"`
	// FailClosed — недоступный store отвечает 503 вместо выполнения запроса без дедупликации.
	FailClosed bool `envconfig:"HTTP_IDEMPOTENCY_FAIL_CLOSED" default:"false"`
}

// IdempotencyStore — байтовый store; запись сериализует httpkit.
// Реализация обязана: Reserve — атомарно занять свободный ключ и вернуть токен
// владельца резерва (ok=false, если ключ занят резервом или записью); Get — не
// отдавать резерв как запись; Release — снять резерв, **только** если ключ всё
// ещё держит этот токен, и не трогать сохранённый ответ. Токен отсекает
// опоздавший Release: резерв, переживший ReserveTTL, не снимает чужой.
type IdempotencyStore interface {
	Reserve(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, record []byte, ttl time.Duration) error
	Release(ctx context.Context, key, token string) error
}

// IdempotencyOption — опция Idempotency.
type IdempotencyOption func(*idempotencyOptions)

type idempotencyOptions struct {
	extract AuthExtractor
}

// WithAuth привязывает идемпотентность к аутентификации: replay отдаётся только
// запросу с валидными credentials, а ключ scope'ится субъектом из ctx
// (ContextWithSubject). Повторную проверку в HumaAuth результат снимает.
func WithAuth(extract AuthExtractor) IdempotencyOption {
	return func(o *idempotencyOptions) { o.extract = extract }
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

// HeaderIdempotentReplayed помечает ответ, воспроизведённый из store.
const HeaderIdempotentReplayed = "Idempotent-Replayed"

const memorySweepEvery = time.Minute

type memoryEntry struct {
	reserved  bool
	token     string
	record    []byte
	expiresAt time.Time
}

// MemoryStore — in-memory IdempotencyStore для тестов и одиночного процесса:
// истёкшие записи выметаются, иначе уникальные ключи растут без границы.
type MemoryStore struct {
	mu        sync.Mutex
	m         map[string]memoryEntry
	now       func() time.Time
	lastSweep time.Time
}

// NewMemoryStore создаёт пустой MemoryStore с системными часами.
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

// sweep амортизирован по записям: ключ идемпотентности уникален, к истёкшей
// записи повторно не обращаются, и без прохода по map она живёт вечно.
func (s *MemoryStore) sweep() {
	t := s.now()
	if t.Sub(s.lastSweep) < memorySweepEvery {
		return
	}
	for k, e := range s.m {
		if !t.Before(e.expiresAt) {
			delete(s.m, k)
		}
	}
	s.lastSweep = t
}

// Reserve занимает ключ, если он свободен, и возвращает токен владельца.
func (s *MemoryStore) Reserve(_ context.Context, key string, ttl time.Duration) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if _, ok := s.live(key); ok {
		return "", false, nil
	}
	token := rand.Text()
	s.m[key] = memoryEntry{reserved: true, token: token, expiresAt: s.now().Add(ttl)}
	return token, true, nil
}

// Get отдаёт сохранённую запись; занятый резервом ключ записью не считается.
func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.live(key)
	if !ok || e.reserved {
		return nil, false, nil
	}
	return e.record, true, nil
}

// Set сохраняет запись поверх резерва на ttl.
func (s *MemoryStore) Set(_ context.Context, key string, record []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	s.m[key] = memoryEntry{record: record, expiresAt: s.now().Add(ttl)}
	return nil
}

// Release снимает резерв владельца токена; чужой резерв и сохранённый ответ
// остаются на месте.
func (s *MemoryStore) Release(_ context.Context, key, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[key]; ok && e.reserved && e.token == token {
		delete(s.m, key)
	}
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

// storeCtx отвязывает запись результата от отмены клиента: клиент отвалился по
// таймауту — ответ всё равно должен сохраниться, иначе его ретрай выполнится дважды.
func storeCtx(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

// Idempotency воспроизводит ответ на повтор POST/PUT/PATCH с тем же ключом.
// Ключ scope'ится субъектом (или Authorization и Cookie клиента); повтор с
// другим телом, путём, query или методом — 422, параллельный повтор — 409.
// Кэшируются только ответы < 500. Ошибка store — fail-open, если не задан
// FailClosed.
func Idempotency(cfg IdempotencyConfig, store IdempotencyStore, opts ...IdempotencyOption) func(http.Handler) http.Handler {
	var o idempotencyOptions
	for _, opt := range opts {
		opt(&o)
	}
	instr := newInstruments()
	outcome := func(ctx context.Context, result string) {
		instr.idempotency.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
	unavailable := func(ctx context.Context, w http.ResponseWriter) bool {
		outcome(ctx, "error")
		if !cfg.FailClosed {
			return false
		}
		writeAPIError(ctx, w, Unavailable("IDEMPOTENCY_UNAVAILABLE", ""))
		return true
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
			// Replay — только аутентифицированному запросу: иначе отозванная
			// сессия с тем же заголовком получила бы чужой сохранённый ответ.
			if o.extract != nil {
				authCtx, err := o.extract(r)
				if err != nil {
					outcome(r.Context(), "skipped")
					next.ServeHTTP(w, r)
					return
				}
				r = r.WithContext(withAuthDone(authCtx))
			}
			ctx := r.Context()

			body, tooBig, err := bufferBody(r, cfg.MaxBodyBytes)
			if err != nil {
				if IsMaxBodyError(err) {
					writeAPIError(ctx, w, Error("BODY_TOO_LARGE", http.StatusRequestEntityTooLarge, "request body too large"))
					return
				}
				writeAPIError(ctx, w, BadRequest("BAD_BODY", "cannot read request body"))
				return
			}
			if tooBig {
				outcome(ctx, "skipped")
				next.ServeHTTP(w, r)
				return
			}
			fp := fingerprint(r, body)
			skey := scopedKey(r, key)

			getCtx, cancelGet := storeCtx(ctx, cfg.StoreTimeout)
			rec, found, err := loadRecord(getCtx, store, skey)
			cancelGet()
			switch {
			case err != nil:
				if unavailable(ctx, w) {
					return
				}
				next.ServeHTTP(w, r)
				return
			case found:
				replay(ctx, w, rec, fp, cfg.Header, outcome)
				return
			}

			reserveCtx, cancelReserve := storeCtx(ctx, cfg.StoreTimeout)
			token, ok, err := store.Reserve(reserveCtx, skey, cfg.ReserveTTL)
			cancelReserve()
			if err != nil {
				if unavailable(ctx, w) {
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				retryCtx, cancelRetry := storeCtx(ctx, cfg.StoreTimeout)
				rec, found, _ := loadRecord(retryCtx, store, skey)
				cancelRetry()
				if found {
					replay(ctx, w, rec, fp, cfg.Header, outcome)
					return
				}
				outcome(ctx, "in_progress")
				writeAPIError(ctx, w, Error("IDEMPOTENCY_IN_PROGRESS", http.StatusConflict,
					"a request with this "+cfg.Header+" is still being processed"))
				return
			}

			release := func() {
				relCtx, cancel := storeCtx(ctx, cfg.StoreTimeout)
				defer cancel()
				_ = store.Release(relCtx, skey, token)
			}
			settled := false
			// panic в handler'е снимает резерв: иначе ретраи получают 409 до конца ReserveTTL.
			defer func() {
				if !settled {
					release()
				}
			}()

			ww := &idempotencyWriter{responseWriter: wrap(w), limit: cfg.MaxBodyBytes}
			next.ServeHTTP(ww, r)
			settled = true

			if ww.status >= http.StatusInternalServerError || ww.overflow {
				release()
				outcome(ctx, "skipped")
				return
			}
			record := idempotencyRecord{Fingerprint: fp, Status: ww.status, Header: http.Header{}, Body: ww.buf.Bytes()}
			for _, h := range replayHeaders {
				if v := ww.Header().Values(h); len(v) > 0 {
					record.Header[h] = v
				}
			}
			raw, err := json.Marshal(record)
			if err == nil {
				setCtx, cancelSet := storeCtx(ctx, cfg.StoreTimeout)
				err = store.Set(setCtx, skey, raw, cfg.TTL)
				cancelSet()
			}
			if err != nil {
				release()
				outcome(ctx, "error")
				return
			}
			outcome(ctx, "stored")
		})
	}
}

func replay(ctx context.Context, w http.ResponseWriter, rec idempotencyRecord, fp, header string, outcome func(context.Context, string)) {
	if rec.Fingerprint != fp {
		outcome(ctx, "conflict")
		e := Error("IDEMPOTENCY_KEY_REUSED", http.StatusUnprocessableEntity, header+" was already used with a different request")
		e.Errors = []FieldError{{Location: "header." + header, Code: e.Code, Message: e.Detail}}
		writeAPIError(ctx, w, e)
		return
	}
	maps.Copy(w.Header(), rec.Header)
	w.Header().Set(HeaderIdempotentReplayed, "true")
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

// fingerprint покрывает метод, путь, канонизированный query и тело: `?dry_run=true`
// — другой запрос, и отдавать ему сохранённый ответ нельзя.
func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Query().Encode()))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// scopedKey привязывает ключ к клиенту: чужой ключ не отдаёт чужой ответ.
// Аутентифицированный субъект устойчивее сырых credentials — ротация токена
// между попытками не превращает ретрай в новую операцию.
func scopedKey(r *http.Request, key string) string {
	h := sha256.New()
	if sub := SubjectFrom(r.Context()); sub != "" {
		h.Write([]byte("sub"))
		h.Write([]byte{0})
		h.Write([]byte(sub))
	} else {
		h.Write([]byte("cred"))
		h.Write([]byte{0})
		h.Write([]byte(r.Header.Get("Authorization")))
		h.Write([]byte{0})
		h.Write([]byte(r.Header.Get("Cookie")))
	}
	h.Write([]byte{0})
	h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))
}
