package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

func TestRegisterAttr_AppearsInLog(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	RegisterAttr(func(ctx context.Context) (string, any, bool) {
		if v, ok := ctx.Value(struct{ k string }{"my_key"}).(string); ok {
			return "my_key", v, true
		}
		return "", nil, false
	})

	var buf bytes.Buffer
	l := NewWithWriter(Config{Level: "debug"}, &buf)

	ctx := context.WithValue(context.Background(), struct{ k string }{"my_key"}, "my_value")
	l.Info(ctx, "test custom attr")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, buf.String())
	}
	if m["my_key"] != "my_value" {
		t.Errorf("expected my_key=my_value in log, got: %v", m)
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
}

func TestNewNotNil(t *testing.T) {
	lp := sdklog.NewLoggerProvider()
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	l := New(Config{Level: "debug"}, lp)
	if l == nil {
		t.Fatal("New returned nil")
	}
}

func TestStdoutSinkWritesJSONWithTrace(t *testing.T) {
	lp := sdklog.NewLoggerProvider()
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	var buf bytes.Buffer
	l := &slogLogger{l: newSlog(Config{Level: "debug", Stdout: true}, lp, &buf)}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	l.Info(trace.ContextWithSpanContext(context.Background(), sc), "stdout sink")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("stdout output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	if m["msg"] != "stdout sink" {
		t.Errorf("msg = %v, want %q", m["msg"], "stdout sink")
	}
	if _, ok := m["trace_id"]; !ok {
		t.Error("missing trace_id in stdout output")
	}
}

func TestStdoutDisabledNoOutput(t *testing.T) {
	lp := sdklog.NewLoggerProvider()
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	var buf bytes.Buffer
	l := &slogLogger{l: newSlog(Config{Level: "debug", Stdout: false}, lp, &buf)}
	l.Info(context.Background(), "should not reach buf")

	if buf.Len() != 0 {
		t.Errorf("stdout sink disabled but buf got: %s", buf.String())
	}
}

func TestNewWithWriterJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	l := NewWithWriter(Config{Level: "debug"}, &buf)
	l.Info(context.Background(), "hello world")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	for _, field := range []string{"msg", "level", "time"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing field %q in JSON output", field)
		}
	}
}

func TestTraceFieldsInLog(t *testing.T) {
	var buf bytes.Buffer
	l := NewWithWriter(Config{Level: "debug"}, &buf)

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	l.Info(ctx, "traced message")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	if _, ok := m["trace_id"]; !ok {
		t.Error("missing trace_id in JSON output")
	}
	if _, ok := m["span_id"]; !ok {
		t.Error("missing span_id in JSON output")
	}
}
