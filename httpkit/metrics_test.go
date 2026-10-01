package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func collectRequests(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http_requests_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("http_requests_total is %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				p, _ := dp.Attributes.Value(attribute.Key("path"))
				s, _ := dp.Attributes.Value(attribute.Key("status"))
				out[p.AsString()+" "+s.AsString()] += dp.Value
			}
		}
	}
	return out
}

func collectSeries(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				method, _ := dp.Attributes.Value(attribute.Key("method"))
				status, _ := dp.Attributes.Value(attribute.Key("status"))
				out[method.AsString()+" "+status.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestMetrics_RoutePatternAndUnmatched(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	r := chi.NewRouter()
	r.Use(metricsWith(newInstrumentsWithMeter(provider.Meter("test"))))
	r.Get("/api/users/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/api/users/123", "/api/users/456", "/scan/admin.php", "/scan/wp-login"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	got := collectRequests(t, reader)
	if got["/api/users/{id} 200"] != 2 {
		t.Errorf("route pattern label: %v", got)
	}
	if got["unmatched 404"] != 2 {
		t.Errorf("404 scans must collapse into one label: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("unexpected label cardinality: %v", got)
	}
}

// Регрессия: метод — токен клиента, поэтому в label попадает только
// стандартное множество, иначе сканер методов растит кардинальность.
func TestMetrics_MethodLabelIsBounded(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	r := chi.NewRouter()
	r.Use(metricsWith(newInstrumentsWithMeter(provider.Meter("test"))))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, method := range []string{"BOGUS1", "BOGUS2", "PROPFIND"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/ok", nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))

	got := collectSeries(t, reader, "http_requests_total")
	if got["other 405"] != 3 {
		t.Errorf("non-standard methods must collapse into `other`: %v", got)
	}
	if got["GET 200"] != 1 || len(got) != 2 {
		t.Errorf("unexpected series: %v", got)
	}
}

// Регрессия: panic доходит до Recovery ниже Metrics, поэтому 500 обязан
// появиться и в http_requests_total, и в access-log.
func TestMetrics_PanicCountedAs500(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	r := chi.NewRouter()
	r.Use(metricsWith(newInstrumentsWithMeter(provider.Meter("test"))))
	r.Use(Recovery(logkit.Nop()))
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic must become 500, got %d", rec.Code)
	}
	if got := collectSeries(t, reader, "http_requests_total"); got["GET 500"] != 1 {
		t.Fatalf("panic request missing from http_requests_total: %v", got)
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
	r.Use(OTel("/ready"))
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
	r.Use(RequestID(), ClientIP(logkit.Nop(), 0), OTel(), Metrics())
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
	r.Use(RequestID(), OTel(), Metrics())
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
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instr := newInstrumentsWithMeter(provider.Meter("test"))
	now := time.Unix(1_700_000_000, 0)

	r := chi.NewRouter()
	r.Use(metricsWith(instr))
	r.Use(CORS(CORSConfig{Origins: []string{"https://app.example"}, Methods: []string{"GET"}, Headers: []string{"Content-Type"}}))
	r.Use(rateLimitWith(newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1}, func() time.Time { return now }), instr))
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

	got := collectRequests(t, reader)
	if got["/api/ping 200"] != 1 {
		t.Errorf("served request missing: %v", got)
	}
	if got["unmatched 204"] != 1 {
		t.Errorf("preflight missing from http_requests_total: %v", got)
	}
	if got["unmatched 429"] != 1 {
		t.Errorf("rate limited request missing from http_requests_total: %v", got)
	}
}
