package bizmetric_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/bizmetric"
)

func TestMustName(t *testing.T) {
	cases := []struct {
		domain, name string
		want         string
		panics       bool
	}{
		{"checkout", "orders_completed", "biz_checkout_orders_completed", false},
		{"signup", "attempts", "biz_signup_attempts", false},
		{"a", "b", "biz_a_b", false},
		{"Checkout", "x", "", true},                // uppercase
		{"checkout", "orders-completed", "", true}, // dash
		{"", "x", "", true},                        // empty
		{"checkout", "", "", true},
		{"1abc", "x", "", true},         // starts with digit
		{"biz_checkout", "x", "", true}, // prefix belongs to the helper
		{"checkout", "biz_x", "", true}, // prefix in name
		{"checkout", "orders_total", "", true},
		{"checkout", "latency_seconds", "", true},
		{"checkout", "orders_count", "", true},                           // Prometheus histogram suffix
		{"checkout", "latency_sum", "", true},                            // Prometheus histogram suffix
		{"checkout", "latency_bucket", "", true},                         // Prometheus histogram suffix
		{"checkout", "orders_created", "", true},                         // Prometheus counter suffix
		{"orders_count", "x", "", true},                                  // suffix in domain
		{"orders_total", "x", "", true},                                  // suffix in domain
		{"checkout", "total_orders", "biz_checkout_total_orders", false}, // suffix word inside name is fine
		{"checkout", "count_of_items", "biz_checkout_count_of_items", false},
	}
	for _, c := range cases {
		t.Run(c.domain+"/"+c.name, func(t *testing.T) {
			defer func() {
				rec := recover()
				if (rec != nil) != c.panics {
					t.Fatalf("panic=%v, want panic=%v", rec, c.panics)
				}
			}()
			got := bizmetric.MustName(c.domain, c.name)
			if !c.panics && got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func collect(t *testing.T, record func(metric.Meter)) metricdata.Metrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	record(mp.Meter("test"))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rm.ScopeMetrics) != 1 || len(rm.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("want exactly one metric, got %+v", rm.ScopeMetrics)
	}
	return rm.ScopeMetrics[0].Metrics[0]
}

func TestCounter(t *testing.T) {
	m := collect(t, func(meter metric.Meter) {
		bizmetric.Counter(meter, "checkout", "orders_completed").Add(context.Background(), 3)
	})
	if m.Name != "biz_checkout_orders_completed_total" {
		t.Fatalf("name: got %q", m.Name)
	}
	if m.Unit != bizmetric.UnitEvent {
		t.Fatalf("unit: got %q, want %q", m.Unit, bizmetric.UnitEvent)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("data: got %T, want Sum[int64]", m.Data)
	}
	if !sum.IsMonotonic {
		t.Fatal("counter must be monotonic")
	}
	if got := sum.DataPoints[0].Value; got != 3 {
		t.Fatalf("value: got %d, want 3", got)
	}
}

func TestHistogram(t *testing.T) {
	m := collect(t, func(meter metric.Meter) {
		bizmetric.Histogram(meter, "onboarding", "duration").Record(context.Background(), 1.5)
	})
	if m.Name != "biz_onboarding_duration_seconds" {
		t.Fatalf("name: got %q", m.Name)
	}
	if m.Unit != bizmetric.UnitSeconds {
		t.Fatalf("unit: got %q, want %q", m.Unit, bizmetric.UnitSeconds)
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("data: got %T, want Histogram[float64]", m.Data)
	}
	// Регрессия: дефолтные бакеты SDK миллисекундные, длительности до 5 с падают в один бакет.
	want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	got := h.DataPoints[0].Bounds
	if len(got) != len(want) {
		t.Fatalf("bounds: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bounds[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestHistogram_CallerOverridesBuckets(t *testing.T) {
	m := collect(t, func(meter metric.Meter) {
		bizmetric.Histogram(meter, "onboarding", "duration",
			metric.WithExplicitBucketBoundaries(1, 2, 3)).Record(context.Background(), 1.5)
	})
	h := m.Data.(metricdata.Histogram[float64])
	if got := h.DataPoints[0].Bounds; len(got) != 3 {
		t.Fatalf("caller options must win over defaults: got bounds %v", got)
	}
}

func TestGauge(t *testing.T) {
	m := collect(t, func(meter metric.Meter) {
		g := bizmetric.Gauge(meter, "checkout", "active_carts")
		g.Record(context.Background(), 7)
		g.Record(context.Background(), 4)
	})
	if m.Name != "biz_checkout_active_carts" {
		t.Fatalf("name: got %q", m.Name)
	}
	if m.Unit != bizmetric.UnitItem {
		t.Fatalf("unit: got %q, want %q", m.Unit, bizmetric.UnitItem)
	}
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("data: got %T, want Gauge[int64]", m.Data)
	}
	// Gauge — последнее записанное значение, не сумма записей.
	if got := g.DataPoints[0].Value; got != 4 {
		t.Fatalf("value: got %d, want 4", got)
	}
}

func TestUpDownCounter(t *testing.T) {
	m := collect(t, func(meter metric.Meter) {
		c := bizmetric.UpDownCounter(meter, "checkout", "carts_open")
		c.Add(context.Background(), 3)
		c.Add(context.Background(), -1)
	})
	if m.Name != "biz_checkout_carts_open" {
		t.Fatalf("name: got %q", m.Name)
	}
	if m.Unit != bizmetric.UnitItem {
		t.Fatalf("unit: got %q, want %q", m.Unit, bizmetric.UnitItem)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("data: got %T, want Sum[int64]", m.Data)
	}
	if sum.IsMonotonic {
		t.Fatal("up-down counter must not be monotonic")
	}
	if got := sum.DataPoints[0].Value; got != 2 {
		t.Fatalf("value: got %d, want 2", got)
	}
}

// failingMeter — meter, чьи конструкторы инструментов всегда ошибаются.
type failingMeter struct{ noop.Meter }

var errMeter = errors.New("instrument registration failed")

func (failingMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return nil, errMeter
}

func (failingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errMeter
}

func (failingMeter) Int64Gauge(string, ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return nil, errMeter
}

func (failingMeter) Int64UpDownCounter(string, ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return nil, errMeter
}

func TestPanicsOnMeterError(t *testing.T) {
	tests := []struct {
		name    string
		create  func(metric.Meter)
		wantMsg string
	}{
		{"counter", func(m metric.Meter) { bizmetric.Counter(m, "checkout", "orders") }, "bizmetric: create counter biz_checkout_orders_total:"},
		{"histogram", func(m metric.Meter) { bizmetric.Histogram(m, "checkout", "latency") }, "bizmetric: create histogram biz_checkout_latency_seconds:"},
		{"gauge", func(m metric.Meter) { bizmetric.Gauge(m, "checkout", "carts") }, "bizmetric: create gauge biz_checkout_carts:"},
		{"updowncounter", func(m metric.Meter) { bizmetric.UpDownCounter(m, "checkout", "carts") }, "bizmetric: create updowncounter biz_checkout_carts:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				rec := recover()
				msg, ok := rec.(string)
				if !ok {
					t.Fatalf("recover: got %v (%T), want string", rec, rec)
				}
				if !strings.HasPrefix(msg, tt.wantMsg) {
					t.Fatalf("panic: got %q, want prefix %q", msg, tt.wantMsg)
				}
			}()
			tt.create(failingMeter{})
		})
	}
}
