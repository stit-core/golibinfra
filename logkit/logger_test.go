package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

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
	l := New(Config{Level: "debug"})
	if l == nil {
		t.Fatal("New returned nil")
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
