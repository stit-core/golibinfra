package logkit

import (
	"context"
	"io"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func BenchmarkLoggerInfoNoCtx(b *testing.B) {
	lg, err := NewWithWriter(Config{Level: "info"}, io.Discard)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "k1", "v1", "k2", 42)
	}
}

func BenchmarkLoggerInfoWithSpan(b *testing.B) {
	lg, err := NewWithWriter(Config{Level: "info"}, io.Discard)
	if err != nil {
		b.Fatal(err)
	}
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

func BenchmarkLoggerInfoRedact(b *testing.B) {
	lg, err := NewWithWriter(Config{Level: "info"}, io.Discard, WithRedact(RedactConfig{Enabled: true}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lg.Info(ctx, "msg", "email", "john@example.com", "elapsed_ns", int64(1234567890123))
	}
}
