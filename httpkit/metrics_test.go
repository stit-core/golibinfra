package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestMetrics_RoutePatternAndUnmatched(t *testing.T) {
	before := metrictest.Collect(t)
	r := chi.NewRouter()
	r.Use(Metrics())
	r.Get("/api/users/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/api/users/123", "/api/users/456", "/scan/admin.php", "/scan/wp-login"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	d := metrictest.Collect(t).Sub(before)
	if got := d.Get(t, "http_requests_total", "method", "GET", "path", "/api/users/{id}", "status", "200"); got.Kind != metrictest.Counter || got.Value != 2 {
		t.Errorf("route pattern label: %v (%s)", got, got.Kind)
	}
	if got := d.Get(t, "http_requests_total", "method", "GET", "path", "unmatched", "status", "404"); got.Value != 2 {
		t.Errorf("404 scans must collapse into one label: %v", got)
	}
	if got := d.Find("http_requests_total"); len(got) != 2 {
		t.Errorf("unexpected label cardinality: %v", got)
	}
	dur := d.Get(t, "http_request_duration_seconds", "method", "GET", "path", "/api/users/{id}", "status", "200")
	if dur.Kind != metrictest.Histogram || dur.Unit != "s" || dur.Count != 2 {
		t.Errorf("http_request_duration_seconds: %v (%s, unit %q), want histogram in s", dur, dur.Kind, dur.Unit)
	}
}

// Регрессия: метод — токен клиента, поэтому в label попадает только
// стандартное множество, иначе сканер методов растит кардинальность.
func TestMetrics_MethodLabelIsBounded(t *testing.T) {
	before := metrictest.Collect(t)
	r := chi.NewRouter()
	r.Use(Metrics())
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, method := range []string{"BOGUS1", "BOGUS2", "PROPFIND"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/ok", nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))

	d := metrictest.Collect(t).Sub(before)
	if got := d.Value("http_requests_total", "method", "other", "status", "405"); got != 3 {
		t.Errorf("non-standard methods must collapse into `other`: %v", d.Find("http_requests_total"))
	}
	if got := d.Value("http_requests_total", "method", "GET", "status", "200"); got != 1 || len(d.Find("http_requests_total")) != 2 {
		t.Errorf("unexpected series: %v", d.Find("http_requests_total"))
	}
}

// Регрессия: panic доходит до Recovery ниже Metrics, поэтому 500 обязан
// появиться и в http_requests_total, и в access-log.
func TestMetrics_PanicCountedAs500(t *testing.T) {
	before := metrictest.Collect(t)
	r := chi.NewRouter()
	r.Use(Metrics())
	r.Use(Recovery(logkit.Nop()))
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic must become 500, got %d", rec.Code)
	}
	d := metrictest.Collect(t).Sub(before)
	if got := d.Value("http_requests_total", "method", "GET", "status", "500"); got != 1 {
		t.Fatalf("panic request missing from http_requests_total: %v", d.Find("http_requests_total"))
	}
	if got := d.Get(t, "http_panics_total", "path", "/boom"); got.Value != 1 {
		t.Fatalf("http_panics_total: %v, want 1", got)
	}
}

// Регрессия: otelhttp повторно зовёт форматтер после роутинга, поэтому имя
// спана должно собираться из chi-паттерна, а не оставаться голым методом.
func TestOTel_SpanNameIsRoutePattern(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r := chi.NewRouter()
	r.Use(OTel(false, "/ready"))
	r.Use(Metrics())
	r.Get("/api/users/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/users/42", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ready", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("quiet path must not open a span: %d spans", len(spans))
	}
	if spans[0].Name() != "GET /api/users/{id}" {
		t.Fatalf("span name = %q, want %q", spans[0].Name(), "GET /api/users/{id}")
	}
	var route, requestID string
	for _, a := range spans[0].Attributes() {
		switch a.Key {
		case "http.route":
			route = a.Value.AsString()
		case "request_id":
			requestID = a.Value.AsString()
		}
	}
	if route != "/api/users/{id}" {
		t.Errorf("http.route attribute = %q", route)
	}
	if requestID != "" {
		t.Errorf("request_id must be empty without RequestID middleware, got %q", requestID)
	}
}

// KOBS-11, KOBS-13: имя спана — маршрут и тогда, когда middleware между OTel и роутером
// заменяет объект запроса (r.WithContext); client.address — IP из ClientIP, а не
// X-Forwarded-For клиента.
func TestOTel_SpanNameAndClientAddressBehindConsumerMiddleware(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	type key struct{}
	r := chi.NewRouter()
	r.Use(RequestID(), ClientIP(0, logkit.Nop()), OTel(false), Metrics())
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), key{}, "v")))
		})
	})
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/items/7", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /items/{id}" {
		t.Fatalf("span names = %v, want [GET /items/{id}]", spanNames(spans))
	}
	var clientAddress string
	for _, a := range spans[0].Attributes() {
		if a.Key == "client.address" {
			clientAddress = a.Value.AsString()
		}
	}
	if clientAddress != "192.0.2.10" {
		t.Fatalf("client.address = %q, want the ClientIP address 192.0.2.10", clientAddress)
	}
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name())
	}
	return names
}

func TestMetrics_SpanCarriesRequestID(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r := chi.NewRouter()
	r.Use(RequestID(), OTel(false), Metrics())
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(HeaderRequestID, "req-42")
	r.ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans: %d", len(spans))
	}
	for _, a := range spans[0].Attributes() {
		if a.Key == "request_id" && a.Value.AsString() == "req-42" {
			return
		}
	}
	t.Fatalf("span must carry request_id: %v", spans[0].Attributes())
}

// Регрессия KOBS-12: RateLimit и CORS стоят ниже Metrics, поэтому 429 и preflight попадают в
// RED-метрики — под маршрутом своего пути, как и 405: роутер паттерн им не выставил, он ищется
// по пути и совпадает с тем, что роутер дал бы хендлеру (свой метод раньше чужих, подроутер,
// слэш на конце паттерна, закодированный сегмент пути). unmatched остаётся только пути без
// маршрута; 405 подроутера идёт под паттерном монтирования.
func TestMetrics_ResponseBeforeRoutingKeepsRoute(t *testing.T) {
	before := metrictest.Collect(t)
	now := time.Unix(1_700_000_000, 0)
	rec := logtest.Capture()

	r := chi.NewRouter()
	r.Use(AccessLog(rec), Metrics())
	r.Use(CORS(CORSConfig{Origins: []string{"https://app.example"}, Methods: []string{"GET"}, Headers: []string{"Content-Type"}}))
	r.Use(rateLimitWith(newRateLimiter(RateLimitConfig{RPS: 1, Burst: 6}, func() time.Time { return now }), newInstruments()))
	r.MethodNotAllowed(methodNotAllowed(r))
	r.Get("/api/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/docs/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/files/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Put("/files/{name}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Route("/v1", func(r chi.Router) {
		r.Post("/users/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	})

	serve := func(method, path string, headers ...string) int {
		req := httptest.NewRequest(method, path, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	want := []struct {
		method, path          string
		headers               []string
		status                int
		labelMethod, labelFor string
	}{
		{http.MethodOptions, "/api/ping", []string{"Origin", "https://app.example", "Access-Control-Request-Method", "GET"}, 204, "OPTIONS", "/api/ping"},
		{http.MethodGet, "/api/ping", nil, 200, "GET", "/api/ping"},
		{http.MethodDelete, "/api/ping", nil, 405, "DELETE", "/api/ping"},
		{"PROPFIND", "/api/ping", nil, 405, "other", "/api/ping"},
		{http.MethodPost, "/v1/users/7", nil, 201, "POST", "/v1/users/{id}"},
		{http.MethodDelete, "/v1/users/7", nil, 405, "DELETE", "/v1/*"},
		{http.MethodGet, "/docs/", nil, 200, "GET", "/docs"},
		{http.MethodDelete, "/v1/users/a%2Fb", nil, 429, "DELETE", "/v1/users/{id}"},
		{http.MethodGet, "/docs/", nil, 429, "GET", "/docs"},
		{http.MethodPut, "/files/logo", nil, 429, "PUT", "/files/{name}"},
		{http.MethodGet, "/scan/wp-login", nil, 429, "GET", unmatched},
	}
	for _, c := range want {
		if got := serve(c.method, c.path, c.headers...); got != c.status {
			t.Fatalf("%s %s = %d, want %d", c.method, c.path, got, c.status)
		}
	}

	d := metrictest.Collect(t).Sub(before)
	access := rec.Entries()
	if len(access) != len(want) {
		t.Fatalf("access log: %d lines for %d requests: %+v", len(access), len(want), access)
	}
	for i, c := range want {
		labels := []string{"method", c.labelMethod, "path", c.labelFor, "status", strconv.Itoa(c.status)}
		if got := d.Get(t, "http_requests_total", labels...); got.Value != 1 {
			t.Errorf("http_requests_total%v = %v, want 1: %v", labels, got.Value, d.Find("http_requests_total"))
		}
		if got := access[i].Str("route"); got != c.labelFor {
			t.Errorf("access log of %s %s (%d): route %q, want %q", c.method, c.path, c.status, got, c.labelFor)
		}
	}
	if got := d.Find("http_requests_total"); len(got) != len(want) {
		t.Errorf("series: %v, want one per request class", got)
	}
	if got := d.Get(t, "http_rate_limited_total"); got.Kind != metrictest.Counter || got.Value != 4 {
		t.Errorf("http_rate_limited_total: %v (%s), want counter delta 4", got, got.Kind)
	}
}

// Route-контекст chi без роутера кладёт в запрос тест хендлера: маршрут искать негде.
func TestRouteLabel_RouteContextWithoutRouter(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/items/1", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	if got := routeLabel(req); got != unmatched {
		t.Fatalf("routeLabel = %q, want %q", got, unmatched)
	}
}

// Регрессия KOBS-12: у спана запроса на путь без маршрута нет http.route (semconv: атрибут — только
// известному маршруту) и имя — метод; ответ до роутера на известный путь — с маршрутом в имени.
func TestMetrics_SpanRouteOnlyForKnownPath(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	now := time.Unix(1_700_000_000, 0)

	r := chi.NewRouter()
	r.Use(RequestID(), OTel(false), Metrics())
	r.Use(rateLimitWith(newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1}, func() time.Time { return now }), newInstruments()))
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/items/1", "/items/2", "/scan/admin.php"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	type span struct {
		name, route string
		hasRoute    bool
	}
	ended := recorder.Ended()
	got := make([]span, 0, len(ended))
	for _, s := range ended {
		sp := span{name: s.Name()}
		for _, a := range s.Attributes() {
			if a.Key == "http.route" {
				sp.route, sp.hasRoute = a.Value.AsString(), true
			}
		}
		got = append(got, sp)
	}
	want := []span{
		{"GET /items/{id}", "/items/{id}", true},
		{"GET /items/{id}", "/items/{id}", true},
		{"GET", "", false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("spans (name, http.route) = %+v, want %+v", got, want)
	}
}
