package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/logkit"
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

// Регрессия: RateLimit и CORS стоят ниже Metrics, поэтому 429 и preflight
// попадают в RED-метрики. Роутинг до них не дошёл — label path у обоих
// `unmatched`.
func TestMetrics_RateLimitedAndPreflightCounted(t *testing.T) {
	before := metrictest.Collect(t)
	now := time.Unix(1_700_000_000, 0)

	r := chi.NewRouter()
	r.Use(Metrics())
	r.Use(CORS(CORSConfig{Origins: []string{"https://app.example"}, Methods: []string{"GET"}, Headers: []string{"Content-Type"}}))
	r.Use(rateLimitWith(newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1}, func() time.Time { return now }), newInstruments()))
	r.Get("/api/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	preflight := httptest.NewRequest(http.MethodOptions, "/api/ping", nil)
	preflight.Header.Set("Origin", "https://app.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", rec.Code)
	}

	var last *httptest.ResponseRecorder
	for range 2 {
		last = httptest.NewRecorder()
		r.ServeHTTP(last, httptest.NewRequest(http.MethodGet, "/api/ping", nil))
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("second request must be rate limited, got %d", last.Code)
	}

	d := metrictest.Collect(t).Sub(before)
	for _, want := range [][2]string{{"/api/ping", "200"}, {"unmatched", "204"}, {"unmatched", "429"}} {
		if got := d.Value("http_requests_total", "path", want[0], "status", want[1]); got != 1 {
			t.Errorf("http_requests_total{path=%s,status=%s} = %v, want 1: %v", want[0], want[1], got, d.Find("http_requests_total"))
		}
	}
	if got := d.Get(t, "http_rate_limited_total"); got.Kind != metrictest.Counter || got.Value != 1 {
		t.Errorf("http_rate_limited_total: %v (%s), want counter delta 1", got, got.Kind)
	}
}
