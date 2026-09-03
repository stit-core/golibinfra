package timerkit

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Глобальный delegate OTel привязывает package-level инструменты к первому
// SetMeterProvider, поэтому reader один на пакет, а тесты сравнивают дельты.
var metricsReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

type metricsSnapshot struct {
	runs      map[string]int64
	durations map[string]uint64
}

func snapshot(t *testing.T) metricsSnapshot {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	s := metricsSnapshot{runs: map[string]int64{}, durations: map[string]uint64{}}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "timer_job_runs_total":
				for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
					job, _ := dp.Attributes.Value(attribute.Key("job"))
					res, _ := dp.Attributes.Value(attribute.Key("result"))
					s.runs[job.AsString()+"/"+res.AsString()] = dp.Value
				}
			case "timer_job_duration_seconds":
				for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
					if dp.Attributes.Len() != 1 {
						t.Errorf("duration labels = %v, want only job", dp.Attributes.ToSlice())
					}
					job, _ := dp.Attributes.Value(attribute.Key("job"))
					s.durations[job.AsString()] = dp.Count
				}
			}
		}
	}
	return s
}
