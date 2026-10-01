package bizmetric_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"git.stit.tech/stit-core/golibinfra/bizmetric"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
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

// recorded — серия name, записанная record через глобальный провайдер (metrictest).
// Scope — имя теста: одноимённые инструменты разных тестов не сливаются.
func recorded(t *testing.T, name string, record func(metric.Meter)) metrictest.Series {
	t.Helper()
	before := metrictest.Collect(t)
	record(otel.Meter(t.Name()))
	return metrictest.Collect(t).Sub(before).Get(t, name)
}

func TestCounter(t *testing.T) {
	s := recorded(t, "biz_checkout_orders_completed_total", func(meter metric.Meter) {
		bizmetric.Counter(meter, "checkout", "orders_completed").Add(context.Background(), 3)
	})
	if s.Kind != metrictest.Counter || s.Unit != bizmetric.UnitEvent || s.Value != 3 {
		t.Fatalf("got %v (%s, unit %q), want counter in %q with value 3", s, s.Kind, s.Unit, bizmetric.UnitEvent)
	}
}

func TestHistogram(t *testing.T) {
	s := recorded(t, "biz_onboarding_duration_seconds", func(meter metric.Meter) {
		bizmetric.Histogram(meter, "onboarding", "duration").Record(context.Background(), 1.5)
	})
	if s.Kind != metrictest.Histogram || s.Unit != bizmetric.UnitSeconds || s.Count != 1 {
		t.Fatalf("got %v (%s, unit %q), want histogram in %q", s, s.Kind, s.Unit, bizmetric.UnitSeconds)
	}
	// Регрессия: дефолтные бакеты SDK миллисекундные, длительности до 5 с падают в один бакет.
	if want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}; !slices.Equal(s.Bounds, want) {
		t.Fatalf("bounds: got %v, want %v", s.Bounds, want)
	}
}

func TestHistogram_CallerOverridesBuckets(t *testing.T) {
	s := recorded(t, "biz_onboarding_duration_seconds", func(meter metric.Meter) {
		bizmetric.Histogram(meter, "onboarding", "duration",
			metric.WithExplicitBucketBoundaries(1, 2, 3)).Record(context.Background(), 1.5)
	})
	if !slices.Equal(s.Bounds, []float64{1, 2, 3}) {
		t.Fatalf("caller options must win over defaults: got bounds %v", s.Bounds)
	}
}

func TestGauge(t *testing.T) {
	s := recorded(t, "biz_checkout_active_carts", func(meter metric.Meter) {
		g := bizmetric.Gauge(meter, "checkout", "active_carts")
		g.Record(context.Background(), 7)
		g.Record(context.Background(), 4)
	})
	// Gauge — последнее записанное значение, не сумма записей.
	if s.Kind != metrictest.Gauge || s.Unit != bizmetric.UnitItem || s.Value != 4 {
		t.Fatalf("got %v (%s, unit %q), want gauge in %q with value 4", s, s.Kind, s.Unit, bizmetric.UnitItem)
	}
}

func TestUpDownCounter(t *testing.T) {
	s := recorded(t, "biz_checkout_carts_open", func(meter metric.Meter) {
		c := bizmetric.UpDownCounter(meter, "checkout", "carts_open")
		c.Add(context.Background(), 3)
		c.Add(context.Background(), -1)
	})
	if s.Kind != metrictest.UpDownCounter || s.Unit != bizmetric.UnitItem || s.Value != 2 {
		t.Fatalf("got %v (%s, unit %q), want up-down counter in %q with value 2", s, s.Kind, s.Unit, bizmetric.UnitItem)
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
