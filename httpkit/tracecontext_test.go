package httpkit_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

const (
	clientTraceID     = "0af7651916cd43dd8448eb211c80319c"
	clientTraceparent = "00-" + clientTraceID + "-b7ad6b7169203331-01"
	clientTracestate  = "vendor=chosen"
	clientBaggage     = "tenant=evil"
)

// traceOutcome — что запрос оставил после себя: trace_id записей лога (хендлер и access-log —
// один и тот же) и заголовки вызова httpclient из хендлера.
type traceOutcome struct {
	logged                           string
	traceparent, tracestate, baggage string
}

// serveTraced проводит запрос через сервер кита с провайдером трасс процесса tp; хендлер пишет
// запись лога и зовёт нижестоящий сервис клиентом кита.
func serveTraced(t *testing.T, trust bool, tp trace.TracerProvider, incoming bool) traceOutcome {
	t.Helper()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	var down http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	clientCfg, err := httpclient.NewConfig()
	if err != nil {
		t.Fatalf("httpclient.NewConfig: %v", err)
	}
	client, err := httpclient.New(clientCfg)
	if err != nil {
		t.Fatalf("httpclient.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := testConfig(t, "127.0.0.1:0")
	cfg.TrustTraceContext = trust
	logs := logtest.Capture()
	s, err := httpkit.NewServer(cfg, logs)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	huma.Register(s.BindAPI(), httpkit.Public(huma.Operation{OperationID: "call", Method: http.MethodGet, Path: "/call"}),
		func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			logs.Info(ctx, "handler")
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			if err != nil {
				return nil, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			return &struct{}{}, resp.Body.Close()
		})

	req := httptest.NewRequest(http.MethodGet, "/call", nil)
	if incoming {
		req.Header.Set("traceparent", clientTraceparent)
		req.Header.Set("tracestate", clientTracestate)
		req.Header.Set("baggage", clientBaggage)
	}
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	handler, okHandler := logs.Find(slog.LevelInfo, "handler")
	access, okAccess := logs.Find(slog.LevelInfo, "http")
	if !okHandler || !okAccess || handler.Str("trace_id") != access.Str("trace_id") {
		t.Fatalf("handler record %v and access-log %v must both exist and carry the same trace_id", handler.KV, access.KV)
	}
	return traceOutcome{
		logged:      handler.Str("trace_id"),
		traceparent: down.Get("traceparent"),
		tracestate:  down.Get("tracestate"),
		baggage:     down.Get("baggage"),
	}
}

// Регрессия KSEC-12 поверх ROBS-02: матрица «доверие × SDK × входящий контекст». Без доверия
// (HTTP_TRUST_TRACE_CONTEXT=false) trace-context клиента не попадает ни в лог, ни в вызов
// нижестоящего сервиса при любом провайдере трасс — и при no-op (OTEL_SDK_DISABLED=true),
// который WithNewRoot не исполняет; с доверием и выключенным SDK контекст уходит вниз как пришёл.
func TestServer_ClientTraceContextMatrix(t *testing.T) {
	type want int
	const (
		nothing     want = iota // trace_id в логе нет, заголовков вниз нет
		ownRoot                 // корень сервера: его trace_id в логе и внизу, значений клиента нет
		clientChild             // трасса клиента продолжена спаном сервера
		clientAsIs              // контекст клиента ушёл вниз как пришёл
	)
	for _, c := range []struct {
		trust, sdk, incoming bool
		want                 want
	}{
		{trust: false, sdk: true, incoming: true, want: ownRoot},
		{trust: false, sdk: true, incoming: false, want: ownRoot},
		{trust: false, sdk: false, incoming: true, want: nothing},
		{trust: false, sdk: false, incoming: false, want: nothing},
		{trust: true, sdk: true, incoming: true, want: clientChild},
		{trust: true, sdk: true, incoming: false, want: ownRoot},
		{trust: true, sdk: false, incoming: true, want: clientAsIs},
		{trust: true, sdk: false, incoming: false, want: nothing},
	} {
		t.Run(fmt.Sprintf("trust=%v/sdk=%v/incoming=%v", c.trust, c.sdk, c.incoming), func(t *testing.T) {
			var tp trace.TracerProvider = tracenoop.NewTracerProvider()
			if c.sdk {
				tp = sdktrace.NewTracerProvider()
			}
			got := serveTraced(t, c.trust, tp, c.incoming)
			var ok bool
			switch c.want {
			case nothing:
				ok = got == traceOutcome{}
			case ownRoot:
				ok = got.logged != "" && got.logged != clientTraceID &&
					strings.HasPrefix(got.traceparent, "00-"+got.logged+"-") && got.tracestate == "" && got.baggage == ""
			case clientChild:
				ok = got.logged == clientTraceID && got.traceparent != clientTraceparent &&
					strings.HasPrefix(got.traceparent, "00-"+clientTraceID+"-") && strings.HasSuffix(got.traceparent, "-01") &&
					got.tracestate == clientTracestate && got.baggage == clientBaggage
			case clientAsIs:
				ok = got == traceOutcome{logged: clientTraceID, traceparent: clientTraceparent, tracestate: clientTracestate, baggage: clientBaggage}
			}
			if !ok {
				t.Fatalf("log trace_id %q, downstream traceparent %q tracestate %q baggage %q; want outcome %d",
					got.logged, got.traceparent, got.tracestate, got.baggage, c.want)
			}
		})
	}
}
