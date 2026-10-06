package httpkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

const clientTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

// tracedRequest проводит запрос с traceparent и baggage клиента через OTel(trust) под сэмплером
// без доли и отдаёт спан сервера и baggage в ctx хендлера.
func tracedRequest(t *testing.T, trust bool) (sdktrace.ReadOnlySpan, string) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(1))), sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	var bag string
	h := OTel(trust)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bag = baggage.FromContext(r.Context()).String()
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("traceparent", "00-"+clientTrace+"-00f067aa0ba902b7-01")
	req.Header.Set("baggage", "tenant=evil")
	h.ServeHTTP(httptest.NewRecorder(), req)
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans: %d, want 1", len(spans))
	}
	return spans[0], bag
}

// Регрессия KSEC-12 (H18): без доверия trace-context клиента — ссылка на спане нового корня,
// baggage в ctx не попадает; с HTTP_TRUST_TRACE_CONTEXT — спан продолжает трассу клиента.
func TestOTel_ClientTraceContextTrust(t *testing.T) {
	span, bag := tracedRequest(t, false)
	if span.SpanContext().TraceID().String() == clientTrace || bag != "" {
		t.Fatalf("untrusted: trace %s, baggage %q; want a new root and no baggage", span.SpanContext().TraceID(), bag)
	}
	if links := span.Links(); len(links) != 1 || links[0].SpanContext.TraceID().String() != clientTrace {
		t.Fatalf("untrusted: links %v, want one link to the client trace", links)
	}
	span, bag = tracedRequest(t, true)
	if span.SpanContext().TraceID().String() != clientTrace || bag != "tenant=evil" {
		t.Fatalf("trusted: trace %s, baggage %q; want the client trace and its baggage", span.SpanContext().TraceID(), bag)
	}
}

// Регрессия N7 (m8-05 валидатора B harden-gate-defaults): спан запроса без маршрута — только метод.
func TestOTel_UnmatchedSpanIsMethodOnly(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	r := chi.NewRouter()
	r.Use(OTel(false), Metrics())
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/scan/admin.php", nil))
	if spans := rec.Ended(); len(spans) != 1 || spans[0].Name() != http.MethodGet {
		t.Fatalf("span names = %v, want [GET]", spanNames(spans))
	}
}

// Регрессия N9: запись X-Forwarded-For доверенного прокси с портом — IP клиента, а не адрес
// соединения (иначе все клиенты делят bucket прокси).
func TestClientIP_ForwardedForWithPort(t *testing.T) {
	var got string
	h := ClientIP(1, logkit.Nop())(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIPFrom(r.Context())
	}))
	for xff, want := range map[string]string{"203.0.113.7:4711": "203.0.113.7", "[2001:db8::7]:443": "2001:db8::7", "198.51.100.1": "198.51.100.1"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:5555"
		req.Header.Set("X-Forwarded-For", xff)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != want {
			t.Errorf("X-Forwarded-For %q: client ip %q, want %q", xff, got, want)
		}
	}
}

// N4 валидатора v1.3.0: тест usecase'а кладёт IP клиента в ctx без middleware — в той же
// канонической форме, что ClientIP.
func TestContextWithClientIP(t *testing.T) {
	if got := ClientIPFrom(ContextWithClientIP(context.Background(), "::ffff:192.0.2.1")); got != "192.0.2.1" {
		t.Fatalf("ClientIPFrom = %q, want 192.0.2.1", got)
	}
	if got := ClientIPFrom(ContextWithClientIP(context.Background(), "not-an-ip")); got != "" {
		t.Fatalf("invalid ip: ClientIPFrom = %q, want empty", got)
	}
}

// f1-07 валидатора B harden-gate-defaults: claim берёт операцию того же METHOD и пути, а не первую
// ожидающую того же метода.
func TestRegistry_ClaimMatchesPath(t *testing.T) {
	a := &huma.Operation{Method: http.MethodGet, Path: "/a"}
	b := &huma.Operation{Method: http.MethodGet, Path: "/b"}
	reg := &registry{pending: []*huma.Operation{a, b}}
	if got := reg.claim(&huma.Operation{Method: http.MethodGet, Path: "/b"}); got != b {
		t.Fatalf("claim(/b) = %v, want the /b operation", got)
	}
	if got := reg.claim(&huma.Operation{Method: http.MethodGet, Path: "/b"}); got != nil {
		t.Fatalf("second claim(/b) = %v, want nil", got)
	}
}

// Ответ, начатый после дедлайна ctx, — 503 дедлайна, кто бы ни дошёл до границы первым: хендлер,
// ответивший на отмену ctx, и хендлер, вернувшийся без ответа, таймер не обгоняют.
func TestDeadlineWriter_ResponseAfterDeadlineIsTheTimeout(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for name, respond := range map[string]func(*deadlineWriter){
		"WriteHeader": func(d *deadlineWriter) { d.WriteHeader(http.StatusGone) },
		"Write":       func(d *deadlineWriter) { _, _ = d.Write([]byte("late")) },
		"Flush":       func(d *deadlineWriter) { d.Flush() },
		"no response": func(*deadlineWriter) {},
	} {
		rec := httptest.NewRecorder()
		d := &deadlineWriter{w: rec, h: http.Header{}, ctx: expired, timeout: time.Second, log: logkit.Nop()}
		respond(d)
		d.finish()
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), codeRequestTimeout) {
			t.Errorf("%s after the deadline: %d %q, want the 503 %s", name, rec.Code, rec.Body.String(), codeRequestTimeout)
		}
	}
}
