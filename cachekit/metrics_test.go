package cachekit

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestObserveHook_RecordsResult(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	hook := newObserveHookWith(provider.Meter("test"), noop.NewTracerProvider().Tracer("test"))

	ctx := context.Background()
	results := []error{nil, redis.Nil, errors.New("boom")}
	for _, res := range results {
		process := hook.ProcessHook(func(context.Context, redis.Cmder) error { return res })
		_ = process(ctx, redis.NewStringCmd(ctx, "get", "k"))
	}
	pipeline := hook.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })
	_ = pipeline(ctx, []redis.Cmder{redis.NewStringCmd(ctx, "set", "k", "v")})

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	cmds := map[string]int64{}
	var bounds []float64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "cache_commands_total":
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("unexpected data type %T", m.Data)
				}
				for _, dp := range sum.DataPoints {
					result, _ := dp.Attributes.Value("result")
					seen[result.AsString()] += dp.Value
					cmd, _ := dp.Attributes.Value("cmd")
					cmds[cmd.AsString()] += dp.Value
				}
			case "cache_command_duration_seconds":
				hist, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("unexpected data type %T", m.Data)
				}
				if m.Unit != "s" {
					t.Errorf("duration unit: got %q, want %q", m.Unit, "s")
				}
				bounds = hist.DataPoints[0].Bounds
			}
		}
	}
	// ok: успешный get и успешный пайплайн.
	for want, count := range map[string]int64{"ok": 2, "miss": 1, "error": 1} {
		if seen[want] != count {
			t.Errorf("result=%s: got %d, want %d (all: %v)", want, seen[want], count, seen)
		}
	}
	if cmds["get"] != 3 || cmds["pipeline"] != 1 {
		t.Errorf("cmd labels: %v, want get=3 pipeline=1", cmds)
	}
	if len(bounds) != 12 || bounds[0] != 0.005 || bounds[len(bounds)-1] != 30 {
		t.Errorf("duration buckets: %v, want otelkit.DurationBuckets()", bounds)
	}
}

func TestObserveHook_Spans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	reader := sdkmetric.NewManualReader()
	hook := newObserveHookWith(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"), tp.Tracer("test"))

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
