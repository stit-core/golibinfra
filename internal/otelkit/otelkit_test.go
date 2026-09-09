package otelkit

import (
	"context"
	"errors"
	"runtime/debug"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestScope(t *testing.T) {
	if got, want := Scope("httpkit"), "git.stit.tech/stit-core/golibinfra/httpkit"; got != want {
		t.Fatalf("Scope: got %q, want %q", got, want)
	}
	if got, want := Scope("hubkit/sync"), "git.stit.tech/stit-core/golibinfra/hubkit/sync"; got != want {
		t.Fatalf("Scope: got %q, want %q", got, want)
	}
}

func TestMeter_ScopeAndVersion(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	Must(Meter("pgkit").Int64Counter("pg_probe_total")).Add(context.Background(), 1)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rm.ScopeMetrics) != 1 {
		t.Fatalf("scope metrics: got %d, want 1", len(rm.ScopeMetrics))
	}
	scope := rm.ScopeMetrics[0].Scope
	if scope.Name != Scope("pgkit") {
		t.Fatalf("scope name: got %q, want %q", scope.Name, Scope("pgkit"))
	}
	if scope.Version != Version() || scope.Version == "" {
		t.Fatalf("scope version: got %q, want %q (non-empty)", scope.Version, Version())
	}
}

func TestTracer_ScopeAndVersion(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	_, span := Tracer("natskit").Start(context.Background(), "publish")
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans: got %d, want 1", len(spans))
	}
	scope := spans[0].InstrumentationScope()
	if scope.Name != Scope("natskit") {
		t.Fatalf("scope name: got %q, want %q", scope.Name, Scope("natskit"))
	}
	if scope.Version != Version() || scope.Version == "" {
		t.Fatalf("scope version: got %q, want %q (non-empty)", scope.Version, Version())
	}
}

func TestDurationBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := Must(mp.Meter("test").Float64Histogram("d_seconds", metric.WithUnit("s"), DurationBuckets()))
	h.Record(context.Background(), 0.3)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	hist, ok := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("data: got %T, want Histogram[float64]", rm.ScopeMetrics[0].Metrics[0].Data)
	}
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	got := hist.DataPoints[0].Bounds
	if len(got) != len(want) {
		t.Fatalf("bounds: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bounds[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestMust(t *testing.T) {
	if got := Must("ok", nil); got != "ok" {
		t.Fatalf("Must: got %q, want %q", got, "ok")
	}
	defer func() {
		r := recover()
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("recover: got %v (%T), want string", r, r)
		}
		if want := "otelkit: instrument: boom"; msg != want {
			t.Fatalf("panic: got %q, want %q", msg, want)
		}
	}()
	Must("", errors.New("boom"))
}

func TestReadVersion(t *testing.T) {
	tests := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{
			name: "no build info",
			read: func() (*debug.BuildInfo, bool) { return nil, false },
			want: DevVersion,
		},
		{
			name: "main module without version",
			read: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Path: ModulePath, Version: "(devel)"}}, true
			},
			want: DevVersion,
		},
		{
			name: "main module tagged",
			read: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Path: ModulePath, Version: "v1.2.3"}}, true
			},
			want: "v1.2.3",
		},
		{
			name: "dependency of a consumer",
			read: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{
					Main: debug.Module{Path: "example.com/service", Version: "v0.1.0"},
					Deps: []*debug.Module{{Path: ModulePath, Version: "v1.4.0"}},
				}, true
			},
			want: "v1.4.0",
		},
		{
			name: "dependency replaced by a local directory",
			read: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{
					Main: debug.Module{Path: "example.com/service", Version: "v0.1.0"},
					Deps: []*debug.Module{{
						Path:    ModulePath,
						Version: "v1.4.0",
						Replace: &debug.Module{Path: "../golibinfra"},
					}},
				}, true
			},
			want: DevVersion,
		},
		{
			name: "not in the dependency graph",
			read: func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Path: "example.com/service", Version: "v0.1.0"}}, true
			},
			want: DevVersion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readVersion(tt.read); got != tt.want {
				t.Fatalf("readVersion: got %q, want %q", got, tt.want)
			}
		})
	}
}
