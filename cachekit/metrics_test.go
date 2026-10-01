package cachekit

import (
	"context"
	"errors"
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
		if !hasAttr(spans[i].Attributes(), "db.system", "redis") {
			t.Errorf("span[%d]: db.system=redis missing: %v", i, spans[i].Attributes())
		}
	}
	if spans[0].Status().Code != codes.Unset || spans[1].Status().Code != codes.Unset {
		t.Error("ok and miss must not be error spans")
	}
	if spans[2].Status().Code != codes.Error || len(spans[2].Events()) == 0 {
		t.Errorf("failed command span: status=%v events=%d", spans[2].Status().Code, len(spans[2].Events()))
	}
	for _, attr := range spans[3].Attributes() {
		if attr.Key == "db.redis.batch_size" && attr.Value.AsInt64() != 2 {
			t.Errorf("batch_size: got %v, want 2", attr.Value.AsInt64())
		}
	}
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if attr.Value.AsString() == "k" {
				t.Errorf("span %q leaks a cache key in %s", span.Name(), attr.Key)
			}
		}
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
