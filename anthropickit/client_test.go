package anthropickit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

type logEntry struct {
	level string
	msg   string
	kv    map[string]any
}

type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLogger) add(level, msg string, kv []any) {
	e := logEntry{level: level, msg: msg, kv: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.kv[k] = kv[i+1]
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.mu.Unlock()
}

func (l *captureLogger) Debug(_ context.Context, msg string, kv ...any) { l.add("debug", msg, kv) }
func (l *captureLogger) Info(_ context.Context, msg string, kv ...any)  { l.add("info", msg, kv) }
func (l *captureLogger) Warn(_ context.Context, msg string, kv ...any)  { l.add("warn", msg, kv) }
func (l *captureLogger) Error(_ context.Context, msg string, kv ...any) { l.add("error", msg, kv) }
func (l *captureLogger) With(...any) logkit.Logger                      { return l }

func (l *captureLogger) Enabled(context.Context, slog.Level) bool { return true }

func (l *captureLogger) find(level, msg string) (logEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == level && e.msg == msg {
			return e, true
		}
	}
	return logEntry{}, false
}

// installOTel подменяет глобальные провайдеры на in-memory SDK: кит читает их
// в New (метрики) и в otelhttp-транспорте (спаны).
func installOTel(t *testing.T) (*sdkmetric.ManualReader, *tracetest.SpanRecorder) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	t.Cleanup(func() {
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})
	return reader, spans
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}

func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok {
			return false
		}
		var s string
		switch got.Type() {
		case attribute.BOOL:
			s = strconv.FormatBool(got.AsBool())
		default:
			s = got.AsString()
		}
		if s != v {
			return false
		}
	}
	return true
}

// counterSum суммирует datapoints счётчика name, у которых атрибуты содержат want.
func counterSum(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) int64 {
	t.Helper()
	var total int64
	for _, sm := range collect(t, reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want Sum[int64]", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				if attrsMatch(dp.Attributes, want) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func histogram(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) (count uint64, sum float64) {
	t.Helper()
	for _, sm := range collect(t, reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is %T, want Histogram[float64]", name, m.Data)
			}
			for _, dp := range h.DataPoints {
				if attrsMatch(dp.Attributes, want) {
					count += dp.Count
					sum += dp.Sum
				}
			}
		}
	}
	return count, sum
}

func newTestClient(t *testing.T, srvURL string, log logkit.Logger, mutate func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		APIKey: "sk-test", DefaultModel: "claude-opus-5", BaseURL: srvURL,
		MaxRetries: 0, Timeout: 30 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg, log, WithBaseURL(srvURL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func userRequest(text string) Request {
	return Request{Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock{Text: text}}}}}
}

const messageJSON = `{
  "id": "msg_01", "type": "message", "role": "assistant", "model": "claude-opus-5",
  "content": [
    {"type": "thinking", "thinking": "", "signature": "sig"},
    {"type": "text", "text": "Paris"},
    {"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}}
  ],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {
    "input_tokens": 100, "output_tokens": 20,
    "cache_creation_input_tokens": 30, "cache_read_input_tokens": 10,
    "cache_creation": {"ephemeral_5m_input_tokens": 20, "ephemeral_1h_input_tokens": 10},
    "output_tokens_details": {"thinking_tokens": 5},
    "server_tool_use": {"web_search_requests": 2, "web_fetch_requests": 1}
  }
}`

// messageCost — стоимость messageJSON на claude-opus-5: 100·5 + 20·25 + 20·6.25 +
// 10·10 + 10·0.5 (micros) + 2 web-search по $10/1000.
const messageCost = USDMicros(500 + 500 + 125 + 100 + 5 + 20_000)

func apiError(status int, typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_123")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":"boom"}}`, typ)
	}
}

func TestComplete_ParsesResponseAndRecordsSignals(t *testing.T) {
	reader, spans := installOTel(t)
	var gotBody map[string]any
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotKey = r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	resp, err := c.Complete(context.Background(), userRequest("Столица Франции?"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotBody["model"] != "claude-opus-5" || gotBody["max_tokens"] != float64(defaultMaxTokensSync) {
		t.Errorf("request body = %v", gotBody)
	}
	if th, _ := gotBody["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Errorf("thinking = %v, want adaptive", gotBody["thinking"])
	}

	if resp.ID != "msg_01" || resp.StopReason != StopEndTurn || resp.Text() != "Paris" {
		t.Errorf("response = %+v", resp)
	}
	if len(resp.Content) != 3 {
		t.Fatalf("content = %+v", resp.Content)
	}
	tu, ok := resp.Content[2].(ToolUseBlock)
	if !ok || tu.Name != "get_weather" || string(tu.Input) != `{"city": "Paris"}` {
		t.Errorf("tool_use = %+v", resp.Content[2])
	}
	want := Usage{InputTokens: 100, OutputTokens: 20, CacheWrite5mTokens: 20, CacheWrite1hTokens: 10, CacheReadTokens: 10, ThinkingTokens: 5, WebSearchRequests: 2, WebFetchRequests: 1}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
	if !resp.CostKnown || resp.Cost != messageCost {
		t.Errorf("cost = %d known=%v, want %d known", resp.Cost, resp.CostKnown, messageCost)
	}

	calls := map[string]string{"model": "claude-opus-5", "op": "complete", "status": "ok", "priced": "true"}
	if got := counterSum(t, reader, "anthropic_api_calls_total", calls); got != 1 {
		t.Errorf("calls_total = %d, want 1", got)
	}
	for kind, n := range map[string]int64{"input": 100, "output": 20, "cache_write": 30, "cache_read": 10, "thinking": 5} {
		if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": kind}); got != n {
			t.Errorf("tokens_total{kind=%s} = %d, want %d", kind, got, n)
		}
	}
	if got := counterSum(t, reader, "anthropic_api_cost_micros_total", map[string]string{"model": "claude-opus-5"}); got != int64(messageCost) {
		t.Errorf("cost_micros_total = %d, want %d", got, messageCost)
	}
	if n, _ := histogram(t, reader, "anthropic_api_latency_seconds", map[string]string{"op": "complete"}); n != 1 {
		t.Errorf("latency count = %d, want 1", n)
	}

	var transport int
	var kit sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		switch s.Name() {
		case "anthropic.messages":
			kit = s
		default:
			transport++
		}
	}
	if transport != 1 {
		t.Errorf("otelhttp spans = %d, want 1", transport)
	}
	if kit == nil {
		t.Fatal("no anthropic.messages span")
	}
	if kit.SpanKind() != trace.SpanKindClient || kit.Status().Code == codes.Error {
		t.Errorf("kit span kind = %v status = %v", kit.SpanKind(), kit.Status())
	}
	wantAttrs := map[attribute.Key]any{
		"gen_ai.system":                  "anthropic",
		"gen_ai.operation.name":          "chat",
		"gen_ai.request.model":           "claude-opus-5",
		"gen_ai.request.max_tokens":      int64(defaultMaxTokensSync),
		"gen_ai.response.model":          "claude-opus-5",
		"gen_ai.response.id":             "msg_01",
		"gen_ai.usage.input_tokens":      int64(140),
		"gen_ai.usage.output_tokens":     int64(20),
		"gen_ai.response.finish_reasons": []string{"end_turn"},
	}
	got := map[attribute.Key]any{}
	for _, a := range kit.Attributes() {
		got[a.Key] = a.Value.AsInterface()
	}
	for k, want := range wantAttrs {
		if !reflect.DeepEqual(got[k], want) {
			t.Errorf("span attr %s = %#v, want %#v", k, got[k], want)
		}
	}
	for k := range got {
		if !strings.HasPrefix(string(k), "gen_ai.") {
			t.Errorf("unexpected span attribute %s — prompts and payloads must never be recorded", k)
		}
	}
}

func TestComplete_UnknownModelIsUnpriced(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.ReplaceAll(messageJSON, "claude-opus-5", "claude-experimental-9")))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	req := userRequest("hi")
	req.Model = "claude-experimental-9"
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.CostKnown || resp.Cost != 0 {
		t.Errorf("unknown model: cost = %d known=%v", resp.Cost, resp.CostKnown)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"priced": "false", "status": "ok"}); got != 1 {
		t.Errorf("calls_total{priced=false} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "input"}); got != 100 {
		t.Errorf("tokens still counted for unpriced model: %d", got)
	}
	if got := counterSum(t, reader, "anthropic_api_cost_micros_total", nil); got != 0 {
		t.Errorf("cost recorded for unpriced model: %d", got)
	}
}

func TestComplete_ClassifiesAPIErrors(t *testing.T) {
	cases := []struct {
		status int
		typ    string
		want   error
	}{
		{400, "invalid_request_error", ErrInvalidRequest},
		{401, "authentication_error", ErrAuth},
		{403, "permission_error", ErrAuth},
		{404, "not_found_error", ErrNotFound},
		{429, "rate_limit_error", ErrRateLimited},
		{529, "overloaded_error", ErrOverloaded},
		{500, "api_error", ErrServer},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			reader, _ := installOTel(t)
			srv := httptest.NewServer(apiError(tc.status, tc.typ))
			t.Cleanup(srv.Close)
			log := &captureLogger{}

			c := newTestClient(t, srv.URL, log, nil)
			_, err := c.Complete(context.Background(), userRequest("hi"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"status": "error", "op": "complete"}); got != 1 {
				t.Errorf("calls_total{status=error} = %d, want 1", got)
			}
			e, ok := log.find("warn", "anthropickit: api error")
			if !ok || e.kv["status"] != tc.status || e.kv["request_id"] != "req_123" || e.kv["type"] != tc.typ {
				t.Errorf("warn log = %+v, ok=%v", e, ok)
			}
		})
	}
}

func TestNew_MaxRetriesZeroMakesSingleRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After-Ms", "1")
		apiError(http.StatusInternalServerError, "api_error")(w, r)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.Complete(context.Background(), userRequest("hi")); !errors.Is(err, ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want 1 (MaxRetries=0 disables SDK retries)", hits.Load())
	}
}

func TestNew_RetriesAreLoggedAtDebug(t *testing.T) {
	var hits atomic.Int32
	var retryHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		retryHeaders = append(retryHeaders, r.Header.Get(retryCountHeader))
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			apiError(http.StatusServiceUnavailable, "api_error")(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)
	log := &captureLogger{}

	c := newTestClient(t, srv.URL, log, func(c *Config) { c.MaxRetries = 1 })
	if _, err := c.Complete(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Complete after retry: %v", err)
	}
	if hits.Load() != 2 || strings.Join(retryHeaders, ",") != "0,1" {
		t.Fatalf("hits = %d, retry headers = %v", hits.Load(), retryHeaders)
	}
	e, ok := log.find("debug", "anthropickit: retrying request")
	if !ok || e.kv["attempt"] != "1" || e.kv["path"] != "/v1/messages" {
		t.Errorf("debug retry log = %+v, ok=%v", e, ok)
	}
}

func TestNew_TimeoutBoundsEachRequest(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, func(c *Config) { c.Timeout = 50 * time.Millisecond })
	start := time.Now()
	_, err := c.Complete(context.Background(), userRequest("hi"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout not applied: took %s", time.Since(start))
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"status": "error"}); got != 1 {
		t.Errorf("calls_total{status=error} = %d, want 1", got)
	}
}

func TestNew_ValidatesConfig(t *testing.T) {
	valid := Config{
		APIKey: "k", BaseURL: "https://api.anthropic.com",
		DefaultModel: "claude-opus-5", MaxRetries: 2, Timeout: time.Minute,
	}
	mutate := func(fn func(*Config)) Config {
		c := valid
		fn(&c)
		return c
	}
	bad := map[string]Config{
		"zero value":       {},
		"no key":           mutate(func(c *Config) { c.APIKey = "" }),
		"no default model": mutate(func(c *Config) { c.DefaultModel = "" }),
		"negative retry":   mutate(func(c *Config) { c.MaxRetries = -1 }),
		"zero timeout":     mutate(func(c *Config) { c.Timeout = 0 }),
		"negative timeout": mutate(func(c *Config) { c.Timeout = -time.Second }),
		"empty base url":   mutate(func(c *Config) { c.BaseURL = "" }),
		"schemeless url":   mutate(func(c *Config) { c.BaseURL = "api.anthropic.com" }),
		"wrong scheme":     mutate(func(c *Config) { c.BaseURL = "ftp://api.anthropic.com" }),
		"no host":          mutate(func(c *Config) { c.BaseURL = "https://" }),
		"not a url":        mutate(func(c *Config) { c.BaseURL = "://\x7f" }),
	}
	for name, cfg := range bad {
		if _, err := New(cfg, nil); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v, want ErrConfig", name, err)
		}
	}
	for name, url := range map[string]string{"http": "http://localhost:8080", "https": "https://api.anthropic.com"} {
		if _, err := New(mutate(func(c *Config) { c.BaseURL = url }), nil); err != nil {
			t.Errorf("%s base url rejected: %v", name, err)
		}
	}
	if _, err := New(valid, nil, WithBaseURL("localhost:8080")); !errors.Is(err, ErrConfig) {
		t.Errorf("WithBaseURL must be validated too: %v", err)
	}
}

func TestCountTokens_RecordsMetricsWithoutTokens(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 42}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	n, err := c.CountTokens(context.Background(), userRequest("hi"))
	if err != nil || n != 42 {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "count_tokens", "status": "ok"}); got != 1 {
		t.Errorf("calls_total{op=count_tokens} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", nil); got != 0 {
		t.Errorf("count_tokens must not add billable tokens, got %d", got)
	}
}

func TestCountTokens_ErrorClassified(t *testing.T) {
	srv := httptest.NewServer(apiError(http.StatusTooManyRequests, "rate_limit_error"))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.CountTokens(context.Background(), userRequest("hi")); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func sse(event, data string) string { return "event: " + event + "\ndata: " + data + "\n\n" }

const streamStart = `{"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}}`

var streamEvents = []string{
	sse("message_start", streamStart),
	sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
	sse("ping", `{"type":"ping"}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`),
	sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
	sse("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"get_weather","input":{}}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`),
	sse("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`),
	sse("content_block_stop", `{"type":"content_block_stop","index":1}`),
	sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":15}}`),
	sse("message_stop", `{"type":"message_stop"}`),
}

func sseServer(t *testing.T, before func(), events []string, after func(r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if before != nil {
			before()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			fl.Flush()
		}
		if after != nil {
			after(r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStream_TranslatesEventsAndFinal(t *testing.T) {
	reader, _ := installOTel(t)
	srv := sseServer(t, nil, streamEvents, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	var types []EventType
	var text, partial string
	for st.Next() {
		ev := st.Event()
		types = append(types, ev.Type)
		switch ev.Type {
		case EventMessageStart:
			if ev.Usage == nil || ev.Usage.InputTokens != 25 || ev.Usage.CacheReadTokens != 4 {
				t.Errorf("message_start usage = %+v", ev.Usage)
			}
		case EventTextDelta:
			text += ev.Text
		case EventToolUseStart:
			if ev.ToolName != "get_weather" || ev.ToolUseID != "toolu_9" {
				t.Errorf("tool_use start = %+v", ev)
			}
		case EventToolUseDelta:
			partial += ev.PartialJSON
		case EventMessageDelta:
			if ev.StopReason != StopToolUse || ev.Usage == nil || ev.Usage.OutputTokens != 15 {
				t.Errorf("message_delta = %+v", ev)
			}
		}
	}
	if err := st.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	wantTypes := []EventType{EventMessageStart, EventOther, EventTextDelta, EventTextDelta, EventBlockStop, EventToolUseStart, EventToolUseDelta, EventToolUseDelta, EventBlockStop, EventMessageDelta, EventDone}
	if fmt.Sprint(types) != fmt.Sprint(wantTypes) {
		t.Errorf("event types = %v, want %v", types, wantTypes)
	}
	if text != "Hello world" || partial != `{"city":"Paris"}` {
		t.Errorf("text = %q partial = %q", text, partial)
	}

	final, err := st.Final()
	if err != nil {
		t.Fatalf("Final: %v", err)
	}
	if final.Text() != "Hello world" || final.StopReason != StopToolUse {
		t.Errorf("final = %+v", final)
	}
	if len(final.Content) != 2 {
		t.Fatalf("final content = %+v", final.Content)
	}
	if tu, ok := final.Content[1].(ToolUseBlock); !ok || string(tu.Input) != `{"city":"Paris"}` {
		t.Errorf("final tool_use = %+v", final.Content[1])
	}
	wantUsage := Usage{InputTokens: 25, OutputTokens: 15, CacheReadTokens: 4}
	if final.Usage != wantUsage {
		t.Errorf("final usage = %+v, want %+v", final.Usage, wantUsage)
	}
	// 25 input @ $5 + 15 output @ $25 + 4 cache-read @ $0.5 per MTok.
	if !final.CostKnown || final.Cost != 125+375+2 {
		t.Errorf("final cost = %d known=%v", final.Cost, final.CostKnown)
	}

	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "ok", "priced": "true"}); got != 1 {
		t.Errorf("calls_total{op=stream,status=ok} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", nil); got != 1 {
		t.Errorf("Close after drain must not double-count: calls_total = %d", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "output"}); got != 15 {
		t.Errorf("tokens_total{kind=output} = %d, want 15", got)
	}
}

func TestStream_CloseBeforeEndRecordsAbandoned(t *testing.T) {
	reader, _ := installOTel(t)
	srv := sseServer(t, nil, streamEvents[:3], func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			t.Error("server never saw the client close the stream")
		}
	})
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	for range 3 {
		if !st.Next() {
			t.Fatalf("Next() = false early: %v", st.Err())
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_ = st.Close()

	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "abandoned"}); got != 1 {
		t.Errorf("calls_total{status=abandoned} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", nil); got != 1 {
		t.Errorf("double Close must not double-count: calls_total = %d", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "input"}); got != 25 {
		t.Errorf("abandoned stream must keep accumulated input tokens: %d", got)
	}
	// message_start несёт 25 input, 4 cache-read и 1 output токен: 125 + 2 + 25 micros.
	if got := counterSum(t, reader, "anthropic_api_cost_micros_total", nil); got != 125+2+25 {
		t.Errorf("abandoned stream cost = %d, want 152", got)
	}
}

func TestStream_InitialRequestErrorSurfacesInErrAndFinal(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(apiError(http.StatusTooManyRequests, "rate_limit_error"))
	t.Cleanup(srv.Close)
	log := &captureLogger{}
	c := newTestClient(t, srv.URL, log, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	defer st.Close()
	if st.Next() {
		t.Fatal("Next() = true on failed request")
	}
	if !errors.Is(st.Err(), ErrRateLimited) {
		t.Fatalf("Err = %v, want ErrRateLimited", st.Err())
	}
	if _, err := st.Final(); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Final err = %v, want ErrRateLimited", err)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "error"}); got != 1 {
		t.Errorf("calls_total{op=stream,status=error} = %d, want 1", got)
	}
	if _, ok := log.find("warn", "anthropickit: api error"); !ok {
		t.Error("stream failure must be logged at warn")
	}
}

func TestStream_CloseWithoutNextOnFailedRequestRecordsError(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(apiError(http.StatusUnauthorized, "authentication_error"))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	_ = st.Close()
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "error"}); got != 1 {
		t.Errorf("calls_total{status=error} = %d, want 1", got)
	}
}

func TestStream_LatencyCoversRequestSetup(t *testing.T) {
	reader, _ := installOTel(t)
	const delay = 80 * time.Millisecond
	srv := sseServer(t, func() { time.Sleep(delay) }, streamEvents, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	for st.Next() {
	}
	_ = st.Close()

	n, sum := histogram(t, reader, "anthropic_api_latency_seconds", map[string]string{"op": "stream"})
	if n != 1 || sum < delay.Seconds() {
		t.Errorf("latency count=%d sum=%.3fs, want 1 and >= %s (measured from before the request)", n, sum, delay)
	}
}

func TestStream_TimeoutBoundsWholeStream(t *testing.T) {
	reader, _ := installOTel(t)
	srv := sseServer(t, nil, streamEvents[:3], func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			t.Error("stream was not cut by the request timeout")
		}
	})
	c := newTestClient(t, srv.URL, nil, func(c *Config) { c.Timeout = 100 * time.Millisecond })

	st := c.Stream(context.Background(), userRequest("hi"))
	defer st.Close()
	start := time.Now()
	for st.Next() {
	}
	if !errors.Is(st.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want DeadlineExceeded", st.Err())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("stream ran %s past the timeout", time.Since(start))
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "error"}); got != 1 {
		t.Errorf("calls_total{op=stream,status=error} = %d, want 1", got)
	}
}

func TestStream_MappingErrorIsErrStream(t *testing.T) {
	reader, _ := installOTel(t)
	c := newTestClient(t, "http://unused", nil, nil)
	type bogus struct{ Block }
	st := c.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: []Block{bogus{}}}}})
	if st.Next() || !errors.Is(st.Err(), ErrInvalidRequest) {
		t.Fatalf("errStream: next=%v err=%v", st.Next(), st.Err())
	}
	if got := st.Event(); got != (StreamEvent{}) {
		t.Errorf("Event() = %+v, want the zero event", got)
	}
	if got := st.Usage(); got != (Usage{}) {
		t.Errorf("Usage() = %+v, want zero", got)
	}
	resp, err := st.Final()
	if resp != nil || !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Final() = %+v, %v", resp, err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", nil); got != 0 {
		t.Errorf("a request that never left must not be counted: %d", got)
	}
}

func TestComplete_KeepsRedactedThinkingForReplay(t *testing.T) {
	const withRedacted = `{
	  "id": "msg_02", "type": "message", "role": "assistant", "model": "claude-opus-5",
	  "content": [
	    {"type": "thinking", "thinking": "visible", "signature": "sig"},
	    {"type": "redacted_thinking", "data": "encrypted-blob"},
	    {"type": "text", "text": "ok"}
	  ],
	  "stop_reason": "end_turn", "stop_sequence": null,
	  "usage": {"input_tokens": 1, "output_tokens": 1}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(withRedacted))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	resp, err := c.Complete(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.Content) != 3 {
		t.Fatalf("content = %+v", resp.Content)
	}
	rt, ok := resp.Content[1].(RedactedThinkingBlock)
	if !ok || rt.Data != "encrypted-blob" {
		t.Fatalf("redacted_thinking must survive into Response.Content: %+v", resp.Content[1])
	}
}

func TestComplete_RefusalCarriesStopDetails(t *testing.T) {
	const refusal = `{
	  "id": "msg_03", "type": "message", "role": "assistant", "model": "claude-opus-5",
	  "content": [], "stop_reason": "refusal", "stop_sequence": null,
	  "stop_details": {"type": "refusal", "category": "cyber", "explanation": "policy"},
	  "usage": {"input_tokens": 1, "output_tokens": 1}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(refusal))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	resp, err := c.Complete(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("refusal must not be an error: %v", err)
	}
	if !resp.IsRefusal() || resp.StopDetails == nil {
		t.Fatalf("response = %+v", resp)
	}
	if resp.StopDetails.Category != "cyber" || resp.StopDetails.Explanation != "policy" {
		t.Errorf("stop details = %+v", resp.StopDetails)
	}
}

func TestComplete_ClassifiesTransientStatuses(t *testing.T) {
	for _, status := range []int{408, 409} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			installOTel(t)
			srv := httptest.NewServer(apiError(status, "api_error"))
			t.Cleanup(srv.Close)
			c := newTestClient(t, srv.URL, nil, nil)
			_, err := c.Complete(context.Background(), userRequest("hi"))
			if !errors.Is(err, ErrServer) {
				t.Fatalf("err = %v, want ErrServer", err)
			}
		})
	}
}

func TestComplete_PricedFollowsTheModelTheCostIsComputedFrom(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON)) // отвечает claude-opus-5
	}))
	t.Cleanup(srv.Close)

	log := &captureLogger{}
	c := newTestClient(t, srv.URL, log, nil)
	req := userRequest("hi")
	req.Model = "my-router-alias"
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !resp.CostKnown || resp.Cost == 0 {
		t.Fatalf("cost comes from the response model: %d known=%v", resp.Cost, resp.CostKnown)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"priced": "true"}); got != 1 {
		t.Errorf("priced must agree with the recorded cost: calls{priced=true} = %d", got)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"model": modelLabelOther}); got != 1 {
		t.Errorf("unknown requested model must collapse into %q: %d", modelLabelOther, got)
	}
	e, ok := log.find("warn", "anthropickit: model is absent from the price table, metric label collapsed")
	if !ok || e.kv["model"] != "my-router-alias" {
		t.Errorf("collapsing the label must be logged once: %+v", e)
	}
}

func TestComplete_ModelLabelWarnsOncePerModel(t *testing.T) {
	installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	log := &captureLogger{}
	c := newTestClient(t, srv.URL, log, nil)
	req := userRequest("hi")
	req.Model = "my-router-alias"
	for range 3 {
		if _, err := c.Complete(context.Background(), req); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	var warns int
	log.mu.Lock()
	for _, e := range log.entries {
		if e.msg == "anthropickit: model is absent from the price table, metric label collapsed" {
			warns++
		}
	}
	log.mu.Unlock()
	if warns != 1 {
		t.Errorf("warn count = %d, want 1 per unknown model", warns)
	}
}

func TestNew_IgnoresSDKEnvironmentDefaults(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-bearer")
	t.Setenv("ANTHROPIC_BASE_URL", "http://wrong.invalid")
	t.Setenv("ANTHROPIC_API_KEY", "env-key")

	var gotAuth, gotKey string
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	if _, err := c.Complete(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("ANTHROPIC_BASE_URL must not win over Config: hits = %d", hits.Load())
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none — Config is the only credential source", gotAuth)
	}
	if gotKey != "sk-test" {
		t.Errorf("x-api-key = %q, want the Config key", gotKey)
	}
}

func TestStream_CanceledContextKeepsAccountedUsage(t *testing.T) {
	reader, _ := installOTel(t)
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, e := range streamEvents[:3] {
			_, _ = io.WriteString(w, e)
			fl.Flush()
		}
		<-r.Context().Done()
		close(released)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, srv.URL, nil, nil)
	st := c.Stream(ctx, userRequest("hi"))
	defer st.Close()

	for range 3 {
		if !st.Next() {
			t.Fatalf("Next() = false early: %v", st.Err())
		}
	}
	cancel()
	for st.Next() {
	}
	<-released

	if !errors.Is(st.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", st.Err())
	}
	if got := st.Usage(); got.InputTokens != 25 || got.CacheReadTokens != 4 {
		t.Errorf("Usage() = %+v, want the tokens accumulated before the cancel", got)
	}
	resp, err := st.Final()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Final err = %v, want context.Canceled", err)
	}
	if resp == nil || resp.Usage.InputTokens != 25 {
		t.Fatalf("Final must return the partial response with usage: %+v", resp)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "error"}); got != 1 {
		t.Errorf("calls_total{status=error} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "input"}); got != 25 {
		t.Errorf("an aborted stream must still be billed: input tokens = %d, want 25", got)
	}
	if got := counterSum(t, reader, "anthropic_api_cost_micros_total", nil); got != 125+2+25 {
		t.Errorf("aborted stream cost = %d, want 152", got)
	}
}

func TestStream_FinalBeforeDrainIsAnError(t *testing.T) {
	installOTel(t)
	srv := sseServer(t, nil, streamEvents, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	defer st.Close()
	if !st.Next() {
		t.Fatalf("Next() = false: %v", st.Err())
	}
	if _, err := st.Final(); !errors.Is(err, ErrStreamNotDrained) {
		t.Fatalf("Final before the drain = %v, want ErrStreamNotDrained", err)
	}
	for st.Next() {
	}
	if _, err := st.Final(); err != nil {
		t.Fatalf("Final after the drain = %v", err)
	}
}

func TestStream_NextAfterCloseKeepsErrStable(t *testing.T) {
	installOTel(t)
	srv := sseServer(t, nil, streamEvents, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	if !st.Next() {
		t.Fatalf("Next() = false: %v", st.Err())
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if st.Next() {
		t.Fatal("Next() must stay false after Close()")
	}
	if err := st.Err(); err != nil {
		t.Fatalf("Err() after Close() = %v, want nil", err)
	}
	if got := st.Usage(); got.InputTokens != 25 {
		t.Errorf("Usage() after Close() = %+v, want the accumulated usage", got)
	}
}

func TestComplete_LumpedCacheCreationIsAccounted(t *testing.T) {
	reader, _ := installOTel(t)
	const lumped = `{
	  "id": "msg_04", "type": "message", "role": "assistant", "model": "claude-opus-5",
	  "content": [{"type": "text", "text": "ok"}],
	  "stop_reason": "end_turn", "stop_sequence": null,
	  "usage": {"input_tokens": 10, "output_tokens": 2, "cache_creation_input_tokens": 40}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(lumped))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	resp, err := c.Complete(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheWrite5mTokens != 40 || resp.Usage.TotalInput() != 50 {
		t.Fatalf("a backend without the TTL split must still be accounted: %+v", resp.Usage)
	}
	// 10·5 + 2·25 + 40·6.25 micros.
	if want := USDMicros(50 + 50 + 250); resp.Cost != want {
		t.Errorf("cost = %d, want %d", resp.Cost, want)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "cache_write"}); got != 40 {
		t.Errorf("cache_write tokens = %d, want 40", got)
	}
}

func TestStream_ThinkingDeltaIsTranslated(t *testing.T) {
	installOTel(t)
	events := []string{
		sse("message_start", streamStart),
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
	srv := sseServer(t, nil, events, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	defer st.Close()
	var thinking strings.Builder
	for st.Next() {
		if ev := st.Event(); ev.Type == EventThinkingDelta {
			thinking.WriteString(ev.Thinking)
		}
	}
	if err := st.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	if thinking.String() != "step" {
		t.Errorf("thinking delta = %q, want %q", thinking.String(), "step")
	}
}

func TestStream_AccumulateErrorIsSurfaced(t *testing.T) {
	reader, _ := installOTel(t)
	events := []string{
		sse("message_start", streamStart),
		// Дельта в блок, которого не было: рассинхрон индексов ломает аккумуляцию.
		sse("content_block_delta", `{"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"boom"}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
	srv := sseServer(t, nil, events, nil)
	c := newTestClient(t, srv.URL, nil, nil)

	st := c.Stream(context.Background(), userRequest("hi"))
	defer st.Close()
	for st.Next() {
	}
	if st.Err() == nil {
		t.Fatal("Err() = nil, want the accumulation failure")
	}
	if _, err := st.Final(); err == nil {
		t.Fatal("Final() must carry the accumulation failure")
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream"}); got != 1 {
		t.Errorf("calls_total{op=stream} = %d, want 1", got)
	}
}
