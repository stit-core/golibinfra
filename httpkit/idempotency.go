package httpkit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/logkit"
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

// IdempotencyStore — байтовый store; запись сериализует httpkit. Контракт — набор
// testkit/idempotencytest. Реализация обязана: Reserve — атомарно занять свободный ключ и вернуть
// токен владельца резерва (ok=false, если ключ занят резервом или записью); Get — не отдавать
// резерв как запись; Set — атомарно положить запись, **только** если ключ всё ещё держит резерв
// этого токена (иначе stored=false, запись не пишется); Release — снять резерв, только если ключ
// держит этот токен, и не трогать сохранённый ответ. Токен отсекает опоздавшего: запрос,
// переживший ReserveTTL, не затирает ни резерв, ни запись следующего.
type IdempotencyStore interface {
	Reserve(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key, token string, record []byte, ttl time.Duration) (stored bool, err error)
	Release(ctx context.Context, key, token string) error
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

// notExecuted — ответы «не выполнено, повторите позже»: повтор с тем же ключом исполняет запрос
// заново, а не получает сохранённый отказ на HTTP_IDEMPOTENCY_TTL.
var notExecuted = []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests}

const memorySweepEvery = time.Minute

// memoryStoreMaxKeys — предел ключей MemoryStore: уникальные ключи клиентов не растят память
// процесса без границы.
const memoryStoreMaxKeys = 100_000

var errMemoryStoreFull = errors.New("httpkit: idempotency memory store is full")

const (
	// scopeMaxKeys — сколько ответов одного субъекта middleware сохраняет за окно TTL: один клиент
	// не занимает store целиком (MemoryStore — десятая часть предела) и не растит Redis без границы.
	scopeMaxKeys = 10_000
	// budgetMaxScopes — предел таблицы бюджетов (≈100 B на scope); scope сверх него не учитывается.
	budgetMaxScopes = 100_000
)

var errScopeBudget = fmt.Errorf("httpkit: idempotency key budget of the client is exhausted (%d keys per TTL)", scopeMaxKeys)

type scopeWindow struct {
	stored int
	until  time.Time
}

// keyBudget считает сохранённые ответы на scope за окно TTL — в памяти процесса: у реплик счёт
// свой, на стыке окон живых записей scope — до двух бюджетов.
type keyBudget struct {
	mu        sync.Mutex
	now       func() time.Time
	window    time.Duration
	limit     int
	maxScopes int
	scopes    map[[sha256.Size]byte]scopeWindow
	lastSweep time.Time
}

func newKeyBudget(window time.Duration) *keyBudget {
	return &keyBudget{
		now: time.Now, window: window, limit: scopeMaxKeys, maxScopes: budgetMaxScopes,
		scopes: map[[sha256.Size]byte]scopeWindow{},
	}
}

func (b *keyBudget) exhausted(scope string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	w, ok := b.scopes[sha256.Sum256([]byte(scope))]
	return ok && b.now().Before(w.until) && w.stored >= b.limit
}

func (b *keyBudget) add(scope string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, t := sha256.Sum256([]byte(scope)), b.now()
	w, ok := b.scopes[id]
	if !ok || !t.Before(w.until) {
		if !ok {
			if t.Sub(b.lastSweep) >= memorySweepEvery {
				for k, v := range b.scopes {
					if !t.Before(v.until) {
						delete(b.scopes, k)
					}
				}
				b.lastSweep = t
			}
			if len(b.scopes) >= b.maxScopes {
				return
			}
		}
		w = scopeWindow{until: t.Add(b.window)}
	}
	w.stored++
	b.scopes[id] = w
}

type memoryEntry struct {
	reserved  bool
	token     string
	record    []byte
	expiresAt time.Time
}

// MemoryStore — in-memory IdempotencyStore для тестов и одиночного процесса: истёкшие записи
// выметаются, ключей — не больше 100 000; новый ключ сверх предела — ошибка Reserve, как у
// недоступного store. Свой предел — cachekit.IdempotencyStore поверх MemoryCache или Redis.
type MemoryStore struct {
	mu        sync.Mutex
	m         map[string]memoryEntry
	maxKeys   int
	now       func() time.Time
	lastSweep time.Time
}

// NewMemoryStore создаёт пустой MemoryStore с системными часами.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: map[string]memoryEntry{}, maxKeys: memoryStoreMaxKeys, now: time.Now}
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
	if len(s.m) >= s.maxKeys {
		return "", false, fmt.Errorf("%w: %d keys", errMemoryStoreFull, len(s.m))
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

// Set сохраняет запись на ttl поверх резерва владельца token; чужой или истёкший резерв —
// stored=false.
func (s *MemoryStore) Set(_ context.Context, key, token string, record []byte, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if e, ok := s.live(key); !ok || !e.reserved || e.token != token {
		return false, nil
	}
	s.m[key] = memoryEntry{record: record, expiresAt: s.now().Add(ttl)}
	return true, nil
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

// Idempotency воспроизводит ответ на повтор POST/PUT/PATCH с тем же ключом. Сервер кита ставит
// её в цепочку huma после HumaAuth: replay получает только запрос, прошедший аутентификацию
// операции. Ключ scope'ится субъектом, которого кладёт extractor схемы (ContextWithSubject).
// Запрос без субъекта — аноним или клиент, чей extractor субъекта не положил, — идёт без
// дедупликации и в store не пишет. Повтор с другим телом, путём, query или методом — 422,
// параллельный повтор — 409. Кэшируются только ответы < 500, кроме 408, 409, 425 и 429, и не
// больше scopeMaxKeys на субъекта за окно TTL: новый ключ сверх бюджета — как отказ store.
// Ошибка store — строка лога и fail-open, если не задан FailClosed. nil-логгер — logkit.Nop().
func Idempotency(cfg IdempotencyConfig, store IdempotencyStore, log logkit.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = logkit.Nop()
	}
	return idempotency(cfg, store, log, newKeyBudget(cfg.TTL))
}

func idempotency(cfg IdempotencyConfig, store IdempotencyStore, log logkit.Logger, budget *keyBudget) func(http.Handler) http.Handler {
	instr := newInstruments()
	outcome := func(ctx context.Context, result string) {
		instr.idempotency.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
	}
	storeFailed := func(ctx context.Context, op string, err error) {
		log.Warn(ctx, "idempotency store failed", "op", op, "error", err, "fail_closed", cfg.FailClosed)
	}
	unavailable := func(ctx context.Context, w http.ResponseWriter, op string, err error) bool {
		storeFailed(ctx, op, err)
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
			ctx := r.Context()
			// Без субъекта scope ключа общий: сохранённый ответ одного клиента получил бы другой, а
			// их записи заняли бы store. Credentials запроса — не scope: с ротацией токена клиент
			// получал бы новый бюджет ключей.
			scope := SubjectFrom(ctx)
			if scope == "" {
				outcome(ctx, "skipped")
				next.ServeHTTP(w, r)
				return
			}

			body, tooBig, err := bufferBody(r, cfg.MaxBodyBytes)
			if err != nil {
				if IsMaxBodyError(err) {
					writeAPIError(ctx, w, Error(codeBodyTooLarge, http.StatusRequestEntityTooLarge, "request body too large"))
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
			skey := scopedKey(scope, key)

			getCtx, cancelGet := storeCtx(ctx, cfg.StoreTimeout)
			rec, found, err := loadRecord(getCtx, store, skey)
			cancelGet()
			switch {
			case err != nil:
				if unavailable(ctx, w, "get", err) {
					return
				}
				next.ServeHTTP(w, r)
				return
			case found:
				replay(ctx, w, rec, fp, cfg.Header, outcome)
				return
			}

			// Бюджет исчерпан — как store, который не берёт новый ключ этого клиента.
			token, ok, err := "", false, errScopeBudget
			if !budget.exhausted(scope) {
				reserveCtx, cancelReserve := storeCtx(ctx, cfg.StoreTimeout)
				token, ok, err = store.Reserve(reserveCtx, skey, cfg.ReserveTTL)
				cancelReserve()
			}
			if err != nil {
				if unavailable(ctx, w, "reserve", err) {
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
				if err := store.Release(relCtx, skey, token); err != nil {
					storeFailed(ctx, "release", err)
				}
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

			if ww.status >= http.StatusInternalServerError || ww.overflow || slices.Contains(notExecuted, ww.status) {
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
			stored := false
			if err == nil {
				setCtx, cancelSet := storeCtx(ctx, cfg.StoreTimeout)
				stored, err = store.Set(setCtx, skey, token, raw, cfg.TTL)
				cancelSet()
			}
			switch {
			case err != nil:
				storeFailed(ctx, "set", err)
				release()
				outcome(ctx, "error")
			case !stored:
				// Резерв истёк раньше ответа: ключ уже ничей или держит повтор — его не затираем.
				log.Warn(ctx, "idempotency reservation expired before the response", "reserve_ttl", cfg.ReserveTTL.String())
				outcome(ctx, "skipped")
			default:
				budget.add(scope)
				outcome(ctx, "stored")
			}
		})
	}
}

// humaMiddleware ставит net/http-middleware в цепочку huma: дальше по цепочке идёт контекст
// huma над запросом и writer'ом, которые отдал middleware.
func humaMiddleware(mw func(http.Handler) http.Handler) func(huma.Context, func(huma.Context)) {
	return func(hc huma.Context, next func(huma.Context)) {
		r, w := unwrapHTTP(hc)
		if r == nil {
			next(hc)
			return
		}
		mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next(humachi.NewContext(hc.Operation(), r, w))
		})).ServeHTTP(w, r.WithContext(hc.Context()))
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

// scopedKey привязывает ключ к субъекту: чужой ключ не отдаёт чужой ответ.
func scopedKey(subject, key string) string {
	h := sha256.New()
	h.Write([]byte("sub\x00" + subject))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))
}
