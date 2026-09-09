package sync_test

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Глобальный delegate OTel привязывает инструменты к первому SetMeterProvider,
// поэтому reader один на пакет, а тесты сравнивают дельты.
var metricsReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

// counterValue — значение счётчика по resource; -1, если серии нет.
func counterValue(t *testing.T, name, resource string) int64 {
	t.Helper()
	for _, m := range collect(t, name) {
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok {
			continue
		}
		for _, dp := range sum.DataPoints {
			if res, _ := dp.Attributes.Value(attribute.Key("resource")); res.AsString() == resource {
				return dp.Value
			}
		}
	}
	return -1
}

// gaugeValue — значение int64-gauge по resource; -1, если серии нет.
func gaugeValue(t *testing.T, name, resource string) int64 {
	t.Helper()
	for _, m := range collect(t, name) {
		g, ok := m.Data.(metricdata.Gauge[int64])
		if !ok {
			continue
		}
		for _, dp := range g.DataPoints {
			if res, _ := dp.Attributes.Value(attribute.Key("resource")); res.AsString() == resource {
				return dp.Value
			}
		}
	}
	return -1
}

func collect(t *testing.T, name string) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				out = append(out, m)
			}
		}
	}
	return out
}
