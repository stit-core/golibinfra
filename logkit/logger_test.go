package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func mustLogger(t *testing.T, cfg Config, w io.Writer, opts ...Option) Logger {
	t.Helper()
	l, err := New(cfg, append(opts, WithWriter(w))...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func mustSlog(t *testing.T, cfg Config, w io.Writer, opts ...Option) *slog.Logger {
	t.Helper()
	l, err := NewSlog(cfg, append(opts, WithWriter(w))...)
	if err != nil {
		t.Fatalf("NewSlog: %v", err)
	}
	return l
}

func decodeLine(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, raw)
	}
	return m
}

func spanCtx(t *testing.T) context.Context {
	t.Helper()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

// requestIDAttr регистрирует ctx-атрибут так же, как это делает httpkit.
func requestIDAttr(t *testing.T, key string) context.Context {
	t.Helper()
	resetAttrs()
	t.Cleanup(resetAttrs)

	type ctxKey struct{}
	RegisterAttr(key, func(ctx context.Context) (slog.Value, bool) {
		if v, ok := ctx.Value(ctxKey{}).(string); ok {
			return slog.StringValue(v), true
		}
		return slog.Value{}, false
	})
	return context.WithValue(context.Background(), ctxKey{}, "req-1")
}

func TestRegisterAttr_AppearsInLog(t *testing.T) {
	ctx := requestIDAttr(t, "request_id")

	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf)
	l.Info(ctx, "test custom attr")

	if m := decodeLine(t, buf.Bytes()); m["request_id"] != "req-1" {
		t.Errorf("expected request_id=req-1 in log, got: %v", m)
	}
}

func TestRegisterAttr_SameKeyReplaces(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	RegisterAttr("user_id", func(context.Context) (slog.Value, bool) {
		return slog.StringValue("first"), true
	})
	RegisterAttr("user_id", func(context.Context) (slog.Value, bool) {
		return slog.StringValue("second"), true
	})

	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug"}, &buf).Info(context.Background(), "dup")

	if n := strings.Count(buf.String(), `"user_id"`); n != 1 {
		t.Fatalf("user_id must appear once, got %d: %s", n, buf.String())
	}
	if m := decodeLine(t, buf.Bytes()); m["user_id"] != "second" {
		t.Errorf("re-registration must replace: %v", m["user_id"])
	}
}

func TestRegisterAttr_RejectsEmptyKeyAndNilFunc(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	for name, call := range map[string]func(){
		"empty key": func() { RegisterAttr("", func(context.Context) (slog.Value, bool) { return slog.Value{}, false }) },
		"nil func":  func() { RegisterAttr("k", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("RegisterAttr must panic")
				}
			}()
			call()
		})
	}
}

func TestCtxAttrs_StayTopLevelUnderGroup(t *testing.T) {
	ctx := requestIDAttr(t, "request_id")
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(spanCtx(t)))

	var buf bytes.Buffer
	sl := mustSlog(t, Config{Level: "debug"}, &buf)
	sl.With("top", 1).WithGroup("req").With("id", "r1").InfoContext(ctx, "grouped", "k", "v")

	m := decodeLine(t, buf.Bytes())
	for _, k := range []string{"request_id", "trace_id", "span_id"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%s must stay top-level under WithGroup: %v", k, m)
		}
	}
	req, _ := m["req"].(map[string]any)
	if req["id"] != "r1" || req["k"] != "v" {
		t.Fatalf("group content lost: %v", m)
	}
	for _, k := range []string{"request_id", "trace_id", "span_id"} {
		if _, ok := req[k]; ok {
			t.Errorf("%s leaked into the group: %v", k, req)
		}
	}
	if m["top"] != float64(1) {
		t.Errorf("top = %v", m["top"])
	}
}

func TestCtxAttrs_StayTopLevelUnderNestedGroups(t *testing.T) {
	ctx := requestIDAttr(t, "request_id")

	var buf bytes.Buffer
	sl := mustSlog(t, Config{Level: "debug"}, &buf)
	sl.WithGroup("req").With("id", "r1").WithGroup("user").InfoContext(ctx, "nested", "name", "n")

	m := decodeLine(t, buf.Bytes())
	if m["request_id"] != "req-1" {
		t.Fatalf("request_id must stay top-level: %v", m)
	}
	req, _ := m["req"].(map[string]any)
	user, _ := req["user"].(map[string]any)
	if req["id"] != "r1" || user["name"] != "n" {
		t.Fatalf("nested groups lost: %v", m)
	}
	if _, ok := user["request_id"]; ok {
		t.Errorf("request_id leaked into the innermost group: %v", user)
	}
}

func TestCtxAttrs_NoSpanNoTraceFields(t *testing.T) {
	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug"}, &buf).Info(context.Background(), "plain")

	m := decodeLine(t, buf.Bytes())
	if _, ok := m["trace_id"]; ok {
		t.Errorf("trace_id must be absent without a span: %v", m)
	}
}

func TestNopDoesNotPanic(t *testing.T) {
	l := Nop()
	ctx := context.Background()
	l.Debug(ctx, "debug")
	l.Info(ctx, "info")
	l.Warn(ctx, "warn")
	l.Error(ctx, "error")
	l.With("k", "v").Info(ctx, "with")
	if l.Enabled(ctx, slog.LevelError) {
		t.Error("Nop must report every level disabled")
	}
}

func TestNewNotNil(t *testing.T) {
	l, err := New(Config{Level: "debug", Silent: true})
	if err != nil || l == nil {
		t.Fatalf("New = %v, %v", l, err)
	}
}

func TestInvalidLevelIsError(t *testing.T) {
	if _, err := New(Config{Level: "loud"}, WithWriter(io.Discard)); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
	if _, err := New(Config{}, WithWriter(io.Discard)); err != nil {
		t.Fatalf("zero-value Config must be valid, got %v", err)
	}
	if err := (Config{Level: "WARN"}).Validate(); err != nil {
		t.Fatalf("Validate(WARN) = %v", err)
	}
}

func TestLevelCutoff(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "warn"}, &buf)
	l.Info(context.Background(), "dropped")
	l.Warn(context.Background(), "kept")
	if m := decodeLine(t, buf.Bytes()); m["msg"] != "kept" {
		t.Fatalf("got %v, want only the warn record", m)
	}
}

func TestEnabled_FollowsLevel(t *testing.T) {
	l := mustLogger(t, Config{Level: "warn"}, io.Discard)
	ctx := context.Background()
	if l.Enabled(ctx, slog.LevelInfo) {
		t.Error("info must be disabled at LOG_LEVEL=warn")
	}
	if !l.Enabled(ctx, slog.LevelError) {
		t.Error("error must be enabled at LOG_LEVEL=warn")
	}
	//nolint:staticcheck // nil ctx — защита от паники в чужом коде
	if l.Enabled(nil, slog.LevelInfo) {
		t.Error("nil ctx must not panic and must respect the level")
	}
	//nolint:staticcheck // nil ctx — защита от паники в чужом коде
	l.Error(nil, "nil ctx must not panic")
}

func TestSilentDiscardsOutput(t *testing.T) {
	l, err := New(Config{Level: "debug", Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	l.Info(context.Background(), "discarded")
}

func TestWithWriterBeatsSilent(t *testing.T) {
	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug", Silent: true}, &buf).Info(context.Background(), "explicit writer")
	if buf.Len() == 0 {
		t.Fatal("WithWriter must win over Silent")
	}
}

func TestJSONOutputShape(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf)
	l.Info(context.Background(), "hello world", "n", 7)

	m := decodeLine(t, buf.Bytes())
	for _, field := range []string{"msg", "level", "time"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON output", field)
		}
	}
	if m["n"] != float64(7) {
		t.Errorf("n = %v, want 7", m["n"])
	}
	ts, _ := m["time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("time %q is not RFC3339 with offset: %v", ts, err)
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug", Format: "text"}, &buf).Info(context.Background(), "hello", "k", "v")

	out := buf.String()
	if !strings.Contains(out, `msg=hello`) || !strings.Contains(out, `k=v`) {
		t.Fatalf("text handler output = %q", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("LOG_FORMAT=text must not emit JSON: %q", out)
	}
}

func TestSource_PointsAtCaller(t *testing.T) {
	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug", Source: true}, &buf).Info(context.Background(), "with source")

	m := decodeLine(t, buf.Bytes())
	src, _ := m["source"].(map[string]any)
	file, _ := src["file"].(string)
	if !strings.HasSuffix(file, "logger_test.go") {
		t.Fatalf("source.file = %v, want the caller file, not the logkit wrapper", src)
	}
}

func TestSource_OffByDefault(t *testing.T) {
	var buf bytes.Buffer
	mustLogger(t, Config{Level: "debug"}, &buf).Info(context.Background(), "no source")
	if _, ok := decodeLine(t, buf.Bytes())["source"]; ok {
		t.Error("source must be absent unless LOG_SOURCE=true")
	}
}

func TestFromSlog_SharesChain(t *testing.T) {
	var buf bytes.Buffer
	sl := mustSlog(t, Config{Level: "debug"}, &buf)
	FromSlog(sl).With("k", "v").Info(spanCtx(t), "wrapped")

	m := decodeLine(t, buf.Bytes())
	if m["k"] != "v" || m["trace_id"] == nil {
		t.Fatalf("FromSlog must reuse the same handler chain: %v", m)
	}
	if FromSlog(nil) != Nop() {
		t.Error("FromSlog(nil) must fall back to Nop()")
	}
}

func TestTraceFieldsInLog(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf)
	l.Info(spanCtx(t), "traced message")

	m := decodeLine(t, buf.Bytes())
	if m["trace_id"] != "0102030405060708090a0b0c0d0e0f10" {
		t.Errorf("trace_id = %v", m["trace_id"])
	}
	if m["span_id"] != "0102030405060708" {
		t.Errorf("span_id = %v", m["span_id"])
	}
}

func TestWithService_AddsStaticFields(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf, WithService(ServiceConfig{Service: "my-svc", Env: "test", Version: "1.2.3"}))
	l.Info(context.Background(), "hello")

	m := decodeLine(t, buf.Bytes())
	for k, want := range map[string]string{"service": "my-svc", "env": "test", "version": "1.2.3"} {
		if m[k] != want {
			t.Errorf("field %q: want %q, got %v", k, want, m[k])
		}
	}
}

func TestWithService_SkipsEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf, WithService(ServiceConfig{Env: "dev"}))
	l.Info(context.Background(), "hello")

	m := decodeLine(t, buf.Bytes())
	if _, ok := m["service"]; ok {
		t.Errorf("empty service must not be written: %v", m)
	}
	if m["env"] != "dev" {
		t.Errorf("env = %v", m["env"])
	}
}

type failHandler struct{ slog.Handler }

func (failHandler) Enabled(context.Context, slog.Level) bool { return true }

func (failHandler) Handle(context.Context, slog.Record) error { return errors.New("sink down") }

func (h failHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h failHandler) WithGroup(string) slog.Handler { return h }

func TestFanout_JoinsSinkErrorsAndSkipsDisabled(t *testing.T) {
	var buf bytes.Buffer
	ok := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	h := newFanout(ok, failHandler{})

	ctx := context.Background()
	if err := h.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "msg", 0)); err == nil {
		t.Fatal("fan-out must report the failing sink")
	}
	if buf.Len() != 0 {
		t.Errorf("sink disabled at its own level must be skipped: %s", buf.String())
	}
	if !h.Enabled(ctx, slog.LevelInfo) {
		t.Error("fan-out is enabled while any sink is enabled")
	}
	if hh := h.WithAttrs([]slog.Attr{slog.String("k", "v")}).WithGroup("g"); hh == nil {
		t.Error("WithAttrs/WithGroup must return a handler")
	}
}

func TestCtxHandler_NoOpWithAttrsAndGroup(t *testing.T) {
	base := slog.NewJSONHandler(io.Discard, nil)
	h := newCtxHandler(base, traceCtxAttrs)
	if h.WithAttrs(nil) != slog.Handler(h) {
		t.Error("WithAttrs(nil) must not build a new handler")
	}
	if h.WithGroup("") != slog.Handler(h) {
		t.Error(`WithGroup("") must not build a new handler`)
	}
}

func TestRegisterAttr_ConcurrentWithHandle(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	l := mustLogger(t, Config{Level: "debug"}, io.Discard)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			RegisterAttr("user_id", func(context.Context) (slog.Value, bool) {
				return slog.StringValue("u"), true
			})
		}
	}()
	for range 200 {
		l.Info(context.Background(), "concurrent")
	}
	<-done
}
