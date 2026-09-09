package logkit

import (
	"context"
	"io"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Глобальный delegate OTel привязывает package-level инструменты к первому
// SetMeterProvider, поэтому reader один на пакет, а тест сравнивает дельты.
var metricsReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}

func logCounts(t *testing.T) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "log_messages_total" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				lvl, _ := dp.Attributes.Value(attribute.Key("level"))
				out[lvl.AsString()] = dp.Value
			}
		}
	}
	return out
}

func assertDelta(t *testing.T, before, after, want map[string]int64) {
	t.Helper()
	for lvl, w := range want {
		if got := after[lvl] - before[lvl]; got != w {
			t.Errorf("log_messages_total{level=%s} delta = %d, want %d", lvl, got, w)
		}
	}
}

func TestLogMessagesTotal_CountsEmittedRecords(t *testing.T) {
	l := mustLogger(t, Config{Level: "debug"}, io.Discard)
	before := logCounts(t)
	ctx := context.Background()
	l.Debug(ctx, "d")
	l.Info(ctx, "i")
	l.Info(ctx, "i")
	l.Warn(ctx, "w")
	l.Error(ctx, "e")

	assertDelta(t, before, logCounts(t), map[string]int64{"debug": 1, "info": 2, "warn": 1, "error": 1})
}

func TestLogMessagesTotal_SkipsFilteredRecords(t *testing.T) {
	l := mustLogger(t, Config{Level: "warn"}, io.Discard)
	before := logCounts(t)
	ctx := context.Background()
	l.Debug(ctx, "d")
	l.Info(ctx, "i")
	l.Warn(ctx, "w")

	assertDelta(t, before, logCounts(t), map[string]int64{"debug": 0, "info": 0, "warn": 1})
}

func TestLogMessagesTotal_CountsSlogPath(t *testing.T) {
	sl := mustSlog(t, Config{Level: "debug"}, io.Discard)
	before := logCounts(t)
	sl.Info("via slog")
	sl.With("k", "v").WithGroup("g").Error("via slog group")

	assertDelta(t, before, logCounts(t), map[string]int64{"info": 1, "error": 1})
}

// Отфильтрованная запись не должна стоить ни одной аллокации: Debug в горячем
// цикле — обычная практика.
func TestLogMessagesTotal_FilteredRecordDoesNotAllocate(t *testing.T) {
	l := mustLogger(t, Config{Level: "info"}, io.Discard)
	ctx := context.Background()
	if got := testing.AllocsPerRun(100, func() { l.Debug(ctx, "hot loop") }); got != 0 {
		t.Errorf("filtered Debug allocates %v times per call, want 0", got)
	}
}
