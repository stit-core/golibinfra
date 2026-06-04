package bizmetric_test

import (
	"context"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.sftbmb.ru/sftbmb-core/golibinfra/bizmetric"
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
		{"1abc", "x", "", true}, // starts with digit
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

func TestCounterEmits(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	counter := bizmetric.Counter(meter, "checkout", "orders_completed")
	counter.Add(context.Background(), 3)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "biz_checkout_orders_completed_total" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("metric biz_checkout_orders_completed_total not emitted; got %+v", rm.ScopeMetrics)
	}
}

func TestHistogramName(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	h := bizmetric.Histogram(meter, "onboarding", "duration")
	h.Record(context.Background(), 1.5)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.HasSuffix(m.Name, "_seconds") && m.Name == "biz_onboarding_duration_seconds" {
				return
			}
		}
	}
	t.Fatalf("biz_onboarding_duration_seconds not found; got %+v", rm.ScopeMetrics)
}
