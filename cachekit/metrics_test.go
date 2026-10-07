package cachekit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func TestObserveHook_RecordsResult(t *testing.T) {
	before := metrictest.Collect(t)
	hook := newObserveHook()

	ctx := context.Background()
	results := []error{nil, redis.Nil, errors.New("boom")}
	for _, res := range results {
		process := hook.ProcessHook(func(context.Context, redis.Cmder) error { return res })
		_ = process(ctx, redis.NewStringCmd(ctx, "get", "k"))
	}
	pipeline := hook.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })
	_ = pipeline(ctx, []redis.Cmder{redis.NewStringCmd(ctx, "set", "k", "v")})

	d := metrictest.Collect(t).Sub(before)
	// ok: успешный get и успешный пайплайн.
	for want, count := range map[string]float64{"ok": 2, "miss": 1, "error": 1} {
		if got := d.Value("cache_commands_total", "result", want); got != count {
			t.Errorf("result=%s: got %v, want %v (all: %v)", want, got, count, d.Find("cache_commands_total"))
		}
	}
	if d.Value("cache_commands_total", "cmd", "get") != 3 || d.Value("cache_commands_total", "cmd", "pipeline") != 1 {
		t.Errorf("cmd labels: %v, want get=3 pipeline=1", d.Find("cache_commands_total"))
	}
	dur := d.Find("cache_command_duration_seconds")
	if len(dur) == 0 || dur[0].Kind != metrictest.Histogram || dur[0].Unit != "s" {
		t.Fatalf("cache_command_duration_seconds: %v, want histogram in s", dur)
	}
	if b := dur[0].Bounds; len(b) != 12 || b[0] != 0.005 || b[len(b)-1] != 30 {
		t.Errorf("duration buckets: %v, want otelkit.DurationBuckets()", b)
	}
}

func TestObserveHook_Spans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	hook := newObserveHook()

	ctx := context.Background()
	ok := hook.ProcessHook(func(context.Context, redis.Cmder) error { return nil })
	_ = ok(ctx, redis.NewStringCmd(ctx, "get", "k"))
	miss := hook.ProcessHook(func(context.Context, redis.Cmder) error { return redis.Nil })
	_ = miss(ctx, redis.NewStringCmd(ctx, "get", "k"))
	failing := hook.ProcessHook(func(context.Context, redis.Cmder) error { return errors.New("boom") })
	_ = failing(ctx, redis.NewStringCmd(ctx, "getdel", "k"))
	pipeline := hook.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })
	_ = pipeline(ctx, []redis.Cmder{redis.NewStatusCmd(ctx, "multi"), redis.NewStatusCmd(ctx, "exec")})

	spans := rec.Ended()
	if len(spans) != 4 {
		t.Fatalf("spans: got %d, want 4", len(spans))
	}
	names := []string{"redis get", "redis get", "redis getdel", "redis pipeline"}
	for i, want := range names {
		if spans[i].Name() != want {
			t.Errorf("span[%d] name: got %q, want %q", i, spans[i].Name(), want)
		}
		if spans[i].SpanKind() != trace.SpanKindClient {
			t.Errorf("span[%d] kind: got %v, want client", i, spans[i].SpanKind())
		}
		// semconv v1.41, как у natskit и httpclient: старые ключи db.system и db.operation не пишутся.
		cmd := strings.TrimPrefix(want, "redis ")
		if !hasAttr(spans[i].Attributes(), "db.system.name", "redis") || !hasAttr(spans[i].Attributes(), "db.operation.name", cmd) ||
			hasKey(spans[i].Attributes(), "db.system") || hasKey(spans[i].Attributes(), "db.operation") {
			t.Errorf("span[%d]: want db.system.name=redis, db.operation.name=%s and no legacy keys: %v", i, cmd, spans[i].Attributes())
		}
	}
	if spans[0].Status().Code != codes.Unset || spans[1].Status().Code != codes.Unset {
		t.Error("ok and miss must not be error spans")
	}
	if spans[2].Status().Code != codes.Error || len(spans[2].Events()) == 0 {
		t.Errorf("failed command span: status=%v events=%d", spans[2].Status().Code, len(spans[2].Events()))
	}
	if got := batchSize(spans[3].Attributes()); got != 2 {
		t.Errorf("pipeline db.operation.batch.size: got %d, want 2", got)
	}
	if got := batchSize(spans[0].Attributes()); got != -1 {
		t.Errorf("single command db.operation.batch.size: got %d, want none", got)
	}
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if attr.Value.AsString() == "k" {
				t.Errorf("span %q leaks a cache key in %s", span.Name(), attr.Key)
			}
		}
	}
}

// Пайплайн из одной команды — не батч (semconv): db.operation.batch.size у него нет.
func TestObserveHook_SingleCommandPipelineIsNotBatch(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx := context.Background()
	pipeline := newObserveHook().ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })
	_ = pipeline(ctx, []redis.Cmder{redis.NewStatusCmd(ctx, "ping")})
	spans := rec.Ended()
	if len(spans) != 1 || batchSize(spans[0].Attributes()) != -1 {
		t.Fatalf("one-command pipeline: spans %d, db.operation.batch.size %d, want none", len(spans), batchSize(spans[0].Attributes()))
	}
}

func hasAttr(attrs []attribute.KeyValue, key, value string) bool {
	for _, a := range attrs {
		if string(a.Key) == key && a.Value.AsString() == value {
			return true
		}
	}
	return false
}

func hasKey(attrs []attribute.KeyValue, key string) bool {
	for _, a := range attrs {
		if string(a.Key) == key {
			return true
		}
	}
	return false
}

// batchSize — значение db.operation.batch.size, -1 — атрибута нет.
func batchSize(attrs []attribute.KeyValue) int64 {
	for _, a := range attrs {
		if a.Key == "db.operation.batch.size" {
			return a.Value.AsInt64()
		}
	}
	return -1
}
