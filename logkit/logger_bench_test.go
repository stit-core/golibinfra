package logkit

import (
	"context"
	"io"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func benchLogger(b *testing.B, cfg Config, opts ...Option) Logger {
	b.Helper()
	lg, err := New(cfg, append(opts, WithWriter(io.Discard))...)
	if err != nil {
		b.Fatal(err)
	}
	return lg
}

func BenchmarkLoggerInfoNoCtx(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "k1", "v1", "k2", 42)
	}
}

func BenchmarkLoggerInfoWithSpan(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"})
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "k1", "v1", "k2", 42)
	}
}

func BenchmarkLoggerDebugFiltered(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"})
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Debug(ctx, "msg", "k1", "v1", "k2", 42)
	}
}

func BenchmarkLoggerInfoRedact(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"}, WithRedact(RedactConfig{}))
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "email", "john@example.com", "elapsed_ns", int64(1234567890123))
	}
}

func BenchmarkLoggerInfoStruct(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"})
	ctx := context.Background()
	doc := map[string]any{"id": "ORD-42", "items": []string{"a", "b"}, "total": 1500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "doc", doc)
	}
}

func BenchmarkLoggerInfoRedactStruct(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"}, WithRedact(RedactConfig{}))
	ctx := context.Background()
	doc := map[string]any{"id": "ORD-42", "items": []string{"a", "b"}, "total": 1500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "doc", doc)
	}
}

// Свой набор идёт мимо pre-filter'а — цена узкой маски против дефолтной.
func BenchmarkLoggerInfoRedactStructNarrowSet(b *testing.B) {
	lg := benchLogger(b, Config{Level: "info"},
		WithRedact(RedactConfig{Patterns: Patterns{`\bsk-ant-[A-Za-z0-9_-]{16,}`}}))
	ctx := context.Background()
	doc := map[string]any{"id": "ORD-42", "items": []string{"a", "b"}, "total": 1500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "doc", doc)
	}
}
