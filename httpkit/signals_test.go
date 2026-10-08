package httpkit_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// signalDoc — таблицы docs/observability.md: контракт сигналов кита, который читает оператор.
type signalDoc struct {
	metrics map[string]docMetric
	values  map[string][]string
	logs    map[string]docLog
}

type docMetric struct {
	kind   metrictest.Kind
	labels []string
}

type docLog struct {
	levels             []slog.Level
	required, optional []string
}

var (
	docToken  = regexp.MustCompile("`([^`]+)`")
	docKinds  = map[string]metrictest.Kind{"Int64 Counter": metrictest.Counter, "Float64 Histogram": metrictest.Histogram}
	docLevels = map[string]slog.Level{"Debug": slog.LevelDebug, "Info": slog.LevelInfo, "Warn": slog.LevelWarn, "Error": slog.LevelError}
)

func tokens(cell string) []string {
	found := docToken.FindAllStringSubmatch(cell, -1)
	out := make([]string, 0, len(found))
	for _, m := range found {
		out = append(out, m[1])
	}
	return out
}

// readSignalDoc разбирает три таблицы доки по первой ячейке заголовка: «Метрика», «Label»,
// «Сообщение».
func readSignalDoc(t *testing.T) signalDoc {
	t.Helper()
	raw, err := os.ReadFile("docs/observability.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := signalDoc{metrics: map[string]docMetric{}, values: map[string][]string{}, logs: map[string]docLog{}}
	table := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "|") {
			table = ""
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if table == "" {
			table = cells[0]
			continue
		}
		if strings.HasPrefix(cells[0], "---") || len(tokens(cells[0])) != 1 {
			continue
		}
		name := tokens(cells[0])[0]
		switch table {
		case "Метрика":
			kind, ok := docKinds[cells[1]]
			if !ok {
				t.Fatalf("docs/observability.md: metric %s has an unknown type %q", name, cells[1])
			}
			doc.metrics[name] = docMetric{kind: kind, labels: tokens(cells[2])}
		case "Label":
			doc.values[name] = tokens(cells[1])
		case "Сообщение":
			var l docLog
			for _, word := range strings.Split(cells[1], ",") {
				level, ok := docLevels[strings.TrimSpace(word)]
				if !ok {
					t.Fatalf("docs/observability.md: log line %q has an unknown level %q", name, word)
				}
				l.levels = append(l.levels, level)
			}
			required, optional, _ := strings.Cut(cells[2], "необязательные:")
			l.required, l.optional = tokens(required), tokens(optional)
			doc.logs[name] = l
		}
	}
	if len(doc.metrics) == 0 || len(doc.values) == 0 || len(doc.logs) == 0 {
		t.Fatalf("docs/observability.md: tables not found: %d metrics, %d labels, %d log lines", len(doc.metrics), len(doc.values), len(doc.logs))
	}
	return doc
}

// signalStand — приложение со всеми классами ответа кита; сбои зависимостей включает тест.
type signalStand struct {
	storeDown, probeDown, probePanics atomic.Bool
	hold                              chan struct{}
	holding                           chan struct{}
}

var errSignalItem = errors.New("item not found")

var signalErrors = httpkit.NewErrorMap(httpkit.ErrorRule{Err: errSignalItem, Status: http.StatusNotFound, Code: "ITEM_NOT_FOUND", Detail: "item not found"})

type signalOrder struct {
	Body struct {
		N int `json:"n"`
	}
}

const signalItemDelay = 15 * time.Millisecond

func (s *signalStand) routes(api huma.API) {
	bearer := []map[string][]string{{"Bearer": {}}}
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "get-item", Method: http.MethodGet, Path: "/items/{id}"}),
		func(context.Context, *struct {
			ID string `path:"id"`
		},
		) (*struct{}, error) {
			time.Sleep(signalItemDelay)
			return nil, nil //nolint:nilnil // тело не нужно
		})
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "get-missing", Method: http.MethodGet, Path: "/missing"}),
		func(context.Context, *struct{}) (*struct{}, error) { return nil, signalErrors.Map(errSignalItem) })
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "get-boom", Method: http.MethodGet, Path: "/boom"}),
		func(context.Context, *struct{}) (*struct{}, error) { return nil, errors.New("pq: connection refused") })
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "get-panic", Method: http.MethodGet, Path: "/panic"}),
		func(context.Context, *struct{}) (*struct{}, error) { panic("boom") })
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "get-slow", Method: http.MethodGet, Path: "/slow"}),
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			<-ctx.Done()
			return nil, nil //nolint:nilnil // ответ не дойдёт: дедлайн запроса уже ответил
		})
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "signup", Method: http.MethodPost, Path: "/signup"}),
		func(context.Context, *struct {
			Body struct {
				Email string `json:"email" format:"email"`
			}
		},
		) (*struct{}, error) {
			return nil, nil //nolint:nilnil // тело не нужно
		})
	huma.Register(api, huma.Operation{OperationID: "get-secret", Method: http.MethodGet, Path: "/secret", Security: bearer},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело не нужно
	huma.Register(api, huma.Operation{OperationID: "create-order", Method: http.MethodPost, Path: "/orders", DefaultStatus: http.StatusCreated, Security: bearer},
		func(_ context.Context, in *signalOrder) (*struct{}, error) {
			if in.Body.N == 99 {
				s.holding <- struct{}{}
				<-s.hold
			}
			return nil, nil //nolint:nilnil // тело не нужно
		})
	huma.Register(api, httpkit.Public(huma.Operation{OperationID: "create-public", Method: http.MethodPost, Path: "/public", DefaultStatus: http.StatusCreated}),
		func(context.Context, *signalOrder) (*struct{}, error) { return nil, nil }) //nolint:nilnil // тело не нужно
}

func (s *signalStand) probe() healthkit.Probe {
	return healthkit.New("dep", func(context.Context) error {
		if s.probePanics.Load() {
			panic("probe state is not initialised")
		}
		if s.probeDown.Load() {
			return errors.New("dial tcp 10.0.0.5: refused")
		}
		return nil
	})
}

func signalExtract(r *http.Request) (context.Context, error) {
	switch r.Header.Get("Authorization") {
	case "Bearer ok":
		return httpkit.ContextWithSubject(r.Context(), "u1"), nil
	case "Bearer down":
		return nil, fmt.Errorf("jwks: %w", httpkit.ErrAuthUnavailable)
	case "Bearer broken":
		return nil, httpkit.Error("AUTH_BROKEN", http.StatusInternalServerError, "")
	}
	return nil, errors.New("bad token")
}

// failingStore — MemoryStore, который отказывает, пока поднят флаг.
type failingStore struct {
	*httpkit.MemoryStore
	down *atomic.Bool
}

func (s failingStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.down.Load() {
		return nil, false, errors.New("redis: connection refused")
	}
	return s.MemoryStore.Get(ctx, key)
}

func (s *signalStand) start(t *testing.T, cfg httpkit.Config, log logkit.Logger) *httpkit.Server {
	t.Helper()
	var srv *httpkit.Server
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		fx.Provide(func() logkit.Logger { return log }),
		fx.Provide(func() httpkit.IdempotencyStore {
			return failingStore{MemoryStore: httpkit.NewMemoryStore(), down: &s.storeDown}
		}),
		httpkit.ProvideSecurityScheme(func() httpkit.SecuritySchemeEntry {
			return httpkit.SecuritySchemeEntry{Name: "Bearer", Scheme: &huma.SecurityScheme{Type: "http", Scheme: "bearer"}, Extract: signalExtract}
		}),
		healthkit.ProvideProbe(s.probe),
		fx.Invoke(s.routes),
		httpkit.Module,
		fx.Populate(&srv),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	})
	return srv
}

// signalCase — запрос и сигналы, которые он обязан оставить: одна серия http_requests_total и
// http_request_duration_seconds, строка access-log и спан.
type signalCase struct {
	id, method, path, body string
	headers                []string
	prepare                func()
	status                 int
	label, route           string
	level                  slog.Level
	// metric — серия своей метрики класса ответа («имя» или «имя label значение»); log — его строка
	// лога и подстроки её полей (ключ, подстрока, …).
	metric []string
	log    []string
}

// signals — наблюдатель сигналов одного приложения: лог, спаны и метрики процесса.
type signals struct {
	t     *testing.T
	doc   signalDoc
	srv   *httpkit.Server
	log   *logtest.Recorder
	spans *tracetest.SpanRecorder
	// results — исходы http_idempotency_total, встреченные за сценарий.
	results map[string]bool
}

func waitFor[T any](t *testing.T, what string, find func() (T, bool)) T {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, ok := find(); ok {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s in 5s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *signals) span(rid string) (sdktrace.ReadOnlySpan, bool) {
	for _, sp := range s.spans.Ended() {
		for _, a := range sp.Attributes() {
			if a.Key == "request_id" && a.Value.AsString() == rid {
				return sp, true
			}
		}
	}
	return nil, false
}

func (s *signals) record(rid, msg string) (logtest.Entry, bool) {
	return s.log.FindFunc(func(e logtest.Entry) bool { return e.Msg == msg && e.Str("request_id") == rid })
}

// run шлёт запрос и сверяет его сигналы; возвращает дельту метрик процесса за запрос.
func (s *signals) run(c signalCase) metrictest.Snapshot {
	t := s.t
	t.Helper()
	if c.prepare != nil {
		c.prepare()
	}
	rid := "sig-" + c.id
	before := metrictest.Collect(t)
	var body io.Reader
	headers := append([]string{"X-Request-ID: " + rid}, c.headers...)
	if c.body != "" {
		body, headers = strings.NewReader(c.body), append(headers, "Content-Type: application/json")
	}
	resp := do(t, s.srv, c.method, c.path, body, headers...)
	if resp.StatusCode != c.status {
		t.Fatalf("%s: %s %s = %d, want %d: %s", c.id, c.method, c.path, resp.StatusCode, c.status, bodyString(t, resp))
	}

	quiet := c.path == "/health" || c.path == "/ready"
	if !quiet {
		sp := waitFor(t, "span of "+c.id, func() (sdktrace.ReadOnlySpan, bool) { return s.span(rid) })
		s.checkSpan(c, sp)
	} else if _, opened := s.span(rid); opened {
		t.Errorf("%s: a quiet path opened a span", c.id)
	}
	access := waitFor(t, "access log of "+c.id, func() (logtest.Entry, bool) { return s.record(rid, "http") })
	s.checkAccessLog(c, access)

	d := metrictest.Collect(t).Sub(before)
	labels := []string{"method", c.label, "path", c.route, "status", strconv.Itoa(c.status)}
	if got := d.Find("http_requests_total"); len(got) != 1 || d.Get(t, "http_requests_total", labels...).Value != 1 {
		t.Errorf("%s: http_requests_total grew by %v, want one request under %v", c.id, got, labels)
	}
	dur := d.Get(t, "http_request_duration_seconds", labels...)
	if dur.Count != 1 || dur.Value < 0 || dur.Value > 10 {
		t.Errorf("%s: http_request_duration_seconds%v: %d observations, sum %v s; want one in seconds", c.id, labels, dur.Count, dur.Value)
	}
	if c.id == "item" && dur.Value < signalItemDelay.Seconds() {
		t.Errorf("%s: duration %v s is shorter than the handler's %s", c.id, dur.Value, signalItemDelay)
	}
	for _, own := range []string{"http_panics_total", "http_max_body_rejections_total", "http_rate_limited_total", "http_idempotency_total"} {
		grew := d.Find(own)
		if len(c.metric) > 0 && c.metric[0] == own {
			if len(grew) != 1 || d.Get(t, own, c.metric[1:]...).Value != 1 {
				t.Errorf("%s: %s grew by %v, want 1 under %v", c.id, own, grew, c.metric[1:])
			}
			continue
		}
		if len(grew) != 0 {
			t.Errorf("%s: %s grew by %v, want nothing", c.id, own, grew)
		}
	}
	for _, series := range d.Find("http_idempotency_total") {
		s.results[series.Labels["result"]] = true
	}
	if len(c.log) > 0 {
		e, ok := s.record(rid, c.log[0])
		if !ok {
			t.Fatalf("%s: no %q record of the request: %+v", c.id, c.log[0], s.log.Entries())
		}
		for i := 1; i+1 < len(c.log); i += 2 {
			if !strings.Contains(e.Str(c.log[i]), c.log[i+1]) {
				t.Errorf("%s: %q record has %s = %q, want it to contain %q", c.id, c.log[0], c.log[i], e.Str(c.log[i]), c.log[i+1])
			}
		}
	}
	return d
}

func (s *signals) checkSpan(c signalCase, sp sdktrace.ReadOnlySpan) {
	s.t.Helper()
	attrs := map[string]string{}
	for _, a := range sp.Attributes() {
		attrs[string(a.Key)] = a.Value.String()
	}
	name, route, routed := c.label, "", false
	if c.route != "unmatched" {
		name, route, routed = c.label+" "+c.route, c.route, true
	}
	got, has := attrs["http.route"]
	if sp.Name() != name || sp.SpanKind() != trace.SpanKindServer || got != route || has != routed || attrs["client.address"] != "127.0.0.1" {
		s.t.Errorf("%s: span %q (%s) with http.route %q (set: %v) and client.address %q; want server span %q with route %q (set: %v) and the peer address",
			c.id, sp.Name(), sp.SpanKind(), got, has, attrs["client.address"], name, route, routed)
	}
}

func (s *signals) checkAccessLog(c signalCase, e logtest.Entry) {
	s.t.Helper()
	ms, err := strconv.ParseFloat(e.Str("duration_ms"), 64)
	if e.Level != c.level || e.Str("method") != c.method || e.Str("path") != c.path || e.Str("route") != c.route ||
		e.Str("status") != strconv.Itoa(c.status) || err != nil || ms < 0 || ms > 10_000 {
		s.t.Errorf("%s: access log %v %+v; want %v with method %s, path %s, route %s, status %d and duration_ms in milliseconds",
			c.id, e.Level, e.KV, c.level, c.method, c.path, c.route, c.status)
	}
	if c.id == "item" && ms < float64(signalItemDelay.Milliseconds()) {
		s.t.Errorf("%s: duration_ms %v is shorter than the handler's %s", c.id, ms, signalItemDelay)
	}
}

// Контракт сигналов (KGATE-13, KTST-11): метрики, строки лога и спаны живого Module — те, что
// описаны в docs/observability.md. Дока — источник: имя, тип, unit, labels и их значения, сообщение,
// уровень и поля читаются из её таблиц, так что расхождение в любую сторону — красный тест.
func TestSignals_MatchObservabilityDoc(t *testing.T) {
	doc := readSignalDoc(t)
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	started := metrictest.Collect(t)

	stand := &signalStand{hold: make(chan struct{}), holding: make(chan struct{}, 1)}
	cfg := testConfig(t, "127.0.0.1:0")
	cfg.MaxBody.MaxBytes = 1024
	cfg.CORS.Origins = []string{"https://app.example"}
	cfg.RateLimit.RPS = 0
	s := &signals{t: t, doc: doc, log: logtest.Capture(), spans: recorder, results: map[string]bool{}}
	s.srv = stand.start(t, cfg, s.log)

	ok, key := "Authorization: Bearer ok", "Idempotency-Key: "
	cases := []signalCase{
		{
			id: "item", method: "GET", path: "/items/1", headers: []string{"X-Forwarded-For: 6.6.6.6"}, status: 204, label: "GET", route: "/items/{id}", level: slog.LevelInfo,
			log: []string{"x-forwarded-for ignored: HTTP_TRUST_PROXY_HOPS=0, client ip is the peer address", "peer", "127.0.0.1"},
		},
		{id: "no-route", method: "GET", path: "/nowhere", status: 404, label: "GET", route: "unmatched", level: slog.LevelWarn},
		{id: "no-method", method: "DELETE", path: "/items/1", status: 405, label: "DELETE", route: "/items/{id}", level: slog.LevelWarn},
		{id: "odd-method", method: "PROPFIND", path: "/items/1", status: 405, label: "other", route: "/items/{id}", level: slog.LevelWarn},
		{
			id: "preflight", method: "OPTIONS", path: "/items/1", headers: []string{"Origin: https://app.example", "Access-Control-Request-Method: GET"},
			status: 204, label: "OPTIONS", route: "/items/{id}", level: slog.LevelInfo,
		},
		{id: "invalid", method: "POST", path: "/signup", body: `{"email":"nope"}`, status: 422, label: "POST", route: "/signup", level: slog.LevelWarn},
		{
			id: "too-large", method: "POST", path: "/signup", body: `{"email":"` + strings.Repeat("a", 2000) + `@b.co"}`, status: 413, label: "POST", route: "/signup",
			level: slog.LevelWarn, metric: []string{"http_max_body_rejections_total", "path", "/signup"},
		},
		{id: "mapped", method: "GET", path: "/missing", status: 404, label: "GET", route: "/missing", level: slog.LevelWarn},
		{
			id: "boom", method: "GET", path: "/boom", status: 500, label: "GET", route: "/boom", level: slog.LevelError,
			log: []string{"handler error", "operation", "get-boom", "status", "500", "code", "INTERNAL", "error", "pq: connection refused"},
		},
		{
			id: "panic", method: "GET", path: "/panic", status: 500, label: "GET", route: "/panic", level: slog.LevelError,
			metric: []string{"http_panics_total", "path", "/panic"}, log: []string{"panic recovered", "panic", "boom", "stack", "signals_test.go"},
		},
		{
			id: "anonymous", method: "GET", path: "/secret", status: 401, label: "GET", route: "/secret", level: slog.LevelWarn,
			log: []string{"auth rejected", "error", "bad token"},
		},
		{
			id: "auth-down", method: "GET", path: "/secret", headers: []string{"Authorization: Bearer down"}, status: 503, label: "GET", route: "/secret",
			level: slog.LevelError, log: []string{"auth backend unavailable", "error", "jwks"},
		},
		{
			id: "auth-broken", method: "GET", path: "/secret", headers: []string{"Authorization: Bearer broken"}, status: 500, label: "GET", route: "/secret",
			level: slog.LevelError, log: []string{"auth failed", "code", "AUTH_BROKEN"},
		},
		{id: "authorized", method: "GET", path: "/secret", headers: []string{ok}, status: 204, label: "GET", route: "/secret", level: slog.LevelInfo},
		{
			id: "stored", method: "POST", path: "/orders", body: `{"n":1}`, headers: []string{ok, key + "k1"}, status: 201, label: "POST", route: "/orders",
			level: slog.LevelInfo, metric: []string{"http_idempotency_total", "result", "stored"},
		},
		{
			id: "replayed", method: "POST", path: "/orders", body: `{"n":1}`, headers: []string{ok, key + "k1"}, status: 201, label: "POST", route: "/orders",
			level: slog.LevelInfo, metric: []string{"http_idempotency_total", "result", "replayed"},
		},
		{
			id: "conflict", method: "POST", path: "/orders", body: `{"n":2}`, headers: []string{ok, key + "k1"}, status: 422, label: "POST", route: "/orders",
			level: slog.LevelWarn, metric: []string{"http_idempotency_total", "result", "conflict"},
		},
		{
			id: "anonymous-key", method: "POST", path: "/orders", body: `{"n":1}`, headers: []string{key + "k9"}, status: 401, label: "POST", route: "/orders",
			level: slog.LevelWarn, log: []string{"auth rejected"},
		},
		{
			id: "skipped", method: "POST", path: "/public", body: `{"n":1}`, headers: []string{key + "k10"}, status: 201, label: "POST", route: "/public",
			level: slog.LevelInfo, metric: []string{"http_idempotency_total", "result", "skipped"},
		},
		{
			id: "store-down", method: "POST", path: "/orders", body: `{"n":3}`, headers: []string{ok, key + "k2"}, prepare: func() { stand.storeDown.Store(true) },
			status: 201, label: "POST", route: "/orders", level: slog.LevelInfo, metric: []string{"http_idempotency_total", "result", "error"},
			log: []string{"idempotency store failed", "op", "get", "error", "redis: connection refused", "fail_closed", "false"},
		},
		{id: "health", method: "GET", path: "/health", prepare: func() { stand.storeDown.Store(false) }, status: 200, label: "GET", route: "/health", level: slog.LevelDebug},
		{id: "ready", method: "GET", path: "/ready", status: 200, label: "GET", route: "/ready", level: slog.LevelDebug},
		{
			id: "ready-down", method: "GET", path: "/ready", prepare: func() { stand.probeDown.Store(true) }, status: 503, label: "GET", route: "/ready",
			level: slog.LevelError, log: []string{"readiness probe failed", "probe", "dep", "error", "refused"},
		},
		{
			id: "ready-panic", method: "GET", path: "/ready", prepare: func() { stand.probePanics.Store(true) }, status: 503, label: "GET", route: "/ready",
			level: slog.LevelError, log: []string{"readiness probe panicked", "probe", "dep", "panic", "not initialised", "stack", "signals_test.go"},
		},
	}
	for _, c := range cases {
		s.run(c)
	}

	// Параллельный повтор с тем же ключом, пока первый запрос выполняется, — 409 in_progress.
	held := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), "POST", "http://"+s.srv.Addr().String()+"/orders", strings.NewReader(`{"n":99}`))
		if err == nil {
			req.Header.Set("Authorization", "Bearer ok")
			req.Header.Set("Idempotency-Key", "k3")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "sig-holder")
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusCreated {
					err = fmt.Errorf("status %d, want 201", resp.StatusCode)
				}
			}
		}
		held <- err
	}()
	<-stand.holding
	retry := s.run(signalCase{
		id: "in-progress", method: "POST", path: "/orders", body: `{"n":99}`, headers: []string{ok, key + "k3"}, status: 409, label: "POST",
		route: "/orders", level: slog.LevelWarn, metric: []string{"http_idempotency_total", "result", "in_progress"},
	})
	close(stand.hold)
	if err := <-held; err != nil {
		t.Fatalf("the request that held the key: %v (retry delta: %v)", err, retry.All())
	}
	waitFor(t, "span of the request that held the key", func() (sdktrace.ReadOnlySpan, bool) { return s.span("sig-holder") })

	// Лимитер — своё приложение: первый запрос занимает единственный токен клиента.
	limiter := &signals{t: t, doc: doc, log: logtest.Capture(), spans: recorder, results: s.results}
	lcfg := testConfig(t, "127.0.0.1:0")
	lcfg.RateLimit = httpkit.RateLimitConfig{RPS: 1, Burst: 1}
	limiter.srv = (&signalStand{}).start(t, lcfg, limiter.log)
	do(t, limiter.srv, "GET", "/items/1", nil, "X-Request-ID: sig-token")
	limiter.run(signalCase{
		id: "limited", method: "GET", path: "/items/1", status: 429, label: "GET", route: "/items/{id}", level: slog.LevelWarn,
		metric: []string{"http_rate_limited_total"},
	})

	// Дедлайн запроса — своё приложение: 50 мс вместо 25 с.
	deadline := &signals{t: t, doc: doc, log: logtest.Capture(), spans: recorder, results: s.results}
	dcfg := testConfig(t, "127.0.0.1:0")
	dcfg.RequestTimeout = 50 * time.Millisecond
	deadline.srv = (&signalStand{}).start(t, dcfg, deadline.log)
	deadline.run(signalCase{
		id: "deadline", method: "GET", path: "/slow", status: 503, label: "GET", route: "/slow", level: slog.LevelError,
		log: []string{"request deadline exceeded", "timeout", "50ms"},
	})

	for _, app := range []*signals{s, limiter, deadline} {
		app.checkRecords(t)
	}
	s.checkInventory(t, metrictest.Collect(t).Sub(started))
}

// checkRecords: каждая строка лога приложения описана в доке — сообщение, уровень, поля; поля
// сверх описанных — только те, что logkit берёт из ctx.
func (s *signals) checkRecords(t *testing.T) {
	t.Helper()
	fromCtx := []string{"request_id", "trace_id", "span_id"}
	for _, e := range s.log.Entries() {
		line, documented := s.doc.logs[e.Msg]
		if !documented {
			t.Errorf("log line %q (%v) is not in docs/observability.md", e.Msg, e.Level)
			continue
		}
		if !slices.Contains(line.levels, e.Level) {
			t.Errorf("log line %q at %v, the doc says %v", e.Msg, e.Level, line.levels)
		}
		keys := slices.Sorted(maps.Keys(e.KV))
		for _, k := range line.required {
			if !slices.Contains(keys, k) {
				t.Errorf("log line %q has no field %q: %v", e.Msg, k, keys)
			}
		}
		for _, k := range keys {
			if !slices.Contains(line.required, k) && !slices.Contains(line.optional, k) && !slices.Contains(fromCtx, k) {
				t.Errorf("log line %q has field %q missing from the doc (%v, optional %v)", e.Msg, k, line.required, line.optional)
			}
		}
	}
	for _, msg := range []string{"http server listening", "http"} {
		if !slices.ContainsFunc(s.log.Entries(), func(e logtest.Entry) bool { return e.Msg == msg }) {
			t.Errorf("no %q record", msg)
		}
	}
}

// checkInventory: метрики кита за сценарий — ровно таблица доки: имя, тип, unit, ключи labels,
// значения из описанных множеств; имена не сталкиваются после трансляции в Prometheus.
func (s *signals) checkInventory(t *testing.T, d metrictest.Snapshot) {
	t.Helper()
	const scope = "git.stit.tech/stit-core/golibinfra/httpkit"
	seen := map[string]bool{}
	for _, series := range d.All() {
		// RED-метрики otelhttp — второй словарь тех же запросов рядом с http_*.
		if strings.HasPrefix(series.Name, "http.server.") {
			t.Errorf("otelhttp metric %s (%s) is emitted next to the kit's http_* metrics", series.Name, series.Scope)
		}
		if series.Scope != scope {
			continue
		}
		seen[series.Name] = true
		m, documented := s.doc.metrics[series.Name]
		if !documented {
			t.Errorf("metric %s is not in docs/observability.md", series.Name)
			continue
		}
		unit := ""
		if strings.HasSuffix(series.Name, "_seconds") {
			unit = "s"
		}
		labels := slices.Sorted(maps.Keys(series.Labels))
		if series.Kind != m.kind || series.Unit != unit || !slices.Equal(labels, slices.Sorted(slices.Values(m.labels))) {
			t.Errorf("metric %s: %s, unit %q, labels %v; the doc says %s, unit %q, labels %v", series.Name, series.Kind, series.Unit, labels, m.kind, unit, m.labels)
		}
		for label, value := range series.Labels {
			switch documentedValues := s.doc.values[label]; label {
			case "path":
				if !strings.HasPrefix(value, "/") && (value != "unmatched" || !slices.Contains(documentedValues, value)) {
					t.Errorf("metric %s: path %q is neither a route pattern nor the documented %v", series.Name, value, documentedValues)
				}
			case "status":
				if code, err := strconv.Atoi(value); err != nil || code < 100 || code > 599 {
					t.Errorf("metric %s: status %q is not a response code", series.Name, value)
				}
			default:
				if !slices.Contains(documentedValues, value) {
					t.Errorf("metric %s: %s=%q is not in the documented set %v", series.Name, label, value, documentedValues)
				}
			}
		}
	}
	for name := range s.doc.metrics {
		if !seen[name] {
			t.Errorf("metric %s from docs/observability.md was not emitted", name)
		}
	}
	for _, r := range s.doc.values["result"] {
		if !s.results[r] {
			t.Errorf("http_idempotency_total result %q from the doc was not emitted: %v", r, slices.Sorted(maps.Keys(s.results)))
		}
	}
	if collisions := d.Collisions(); len(collisions) > 0 {
		t.Errorf("metric names collide in Prometheus: %v", collisions)
	}
}

// Каждая строка лога в коде кита описана в доке со своим уровнем, и наоборот: строку, которую
// сценарий выше не вызывает (падение Serve, дедлайн остановки), держит эта сверка.
func TestSignals_LogLinesMatchObservabilityDoc(t *testing.T) {
	doc := readSignalDoc(t)
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	code := map[string][]slog.Level{}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			level, ok := docLevels[sel.Sel.Name]
			msg, isLit := call.Args[1].(*ast.BasicLit)
			if !ok || !isLit || msg.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(msg.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", msg.Value, err)
			}
			if !slices.Contains(code[text], level) {
				code[text] = append(code[text], level)
			}
			return true
		})
	}
	for msg, levels := range code {
		slices.Sort(levels)
		documented := slices.Sorted(slices.Values(doc.logs[msg].levels))
		if !slices.Equal(levels, documented) {
			t.Errorf("log line %q: the code writes it at %v, docs/observability.md says %v", msg, levels, documented)
		}
	}
	for msg := range doc.logs {
		if _, ok := code[msg]; !ok {
			t.Errorf("log line %q from docs/observability.md is not written by the kit", msg)
		}
	}
}
