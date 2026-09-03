package logkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func mustLogger(t *testing.T, cfg Config, w io.Writer, opts ...Option) Logger {
	t.Helper()
	l, err := NewWithWriter(cfg, w, opts...)
	if err != nil {
		t.Fatalf("NewWithWriter: %v", err)
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

func TestRegisterAttr_AppearsInLog(t *testing.T) {
	resetAttrs()
	t.Cleanup(resetAttrs)

	type key struct{ k string }
	RegisterAttr(func(ctx context.Context) (string, any, bool) {
		if v, ok := ctx.Value(key{"my_key"}).(string); ok {
			return "my_key", v, true
		}
		return "", nil, false
	})

	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf)
	l.Info(context.WithValue(context.Background(), key{"my_key"}, "my_value"), "test custom attr")

	if m := decodeLine(t, buf.Bytes()); m["my_key"] != "my_value" {
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
	l, err := New(Config{Level: "debug"})
	if err != nil || l == nil {
		t.Fatalf("New = %v, %v", l, err)
	}
}

func TestInvalidLevelIsError(t *testing.T) {
	if _, err := NewWithWriter(Config{Level: "loud"}, io.Discard); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
	if _, err := NewWithWriter(Config{}, io.Discard); err != nil {
		t.Fatalf("empty level must default to info, got %v", err)
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

func TestStdoutDisabledNoOutput(t *testing.T) {
	l, err := New(Config{Level: "debug", Stdout: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Info(context.Background(), "discarded")
}

func TestNewWithWriterJSONOutput(t *testing.T) {
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
}

func TestTraceFieldsInLog(t *testing.T) {
	var buf bytes.Buffer
	l := mustLogger(t, Config{Level: "debug"}, &buf)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	l.Info(trace.ContextWithSpanContext(context.Background(), sc), "traced message")

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
