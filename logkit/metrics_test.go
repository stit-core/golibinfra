package logkit

import (
	"context"
	"io"
	"testing"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

func assertDelta(t *testing.T, before metrictest.Snapshot, want map[string]float64) {
	t.Helper()
	d := metrictest.Collect(t).Sub(before)
	for lvl, w := range want {
		if got := d.Value("log_messages_total", "level", lvl); got != w {
			t.Errorf("log_messages_total{level=%s} delta = %v, want %v", lvl, got, w)
		}
	}
}

func TestLogMessagesTotal_CountsEmittedRecords(t *testing.T) {
	before := metrictest.Collect(t)
	l := mustLogger(t, Config{Level: "debug"}, io.Discard)
	ctx := context.Background()
	l.Debug(ctx, "d")
	l.Info(ctx, "i")
	l.Info(ctx, "i")
	l.Warn(ctx, "w")
	l.Error(ctx, "e")

	assertDelta(t, before, map[string]float64{"debug": 1, "info": 2, "warn": 1, "error": 1})
	if s := metrictest.Collect(t).Get(t, "log_messages_total", "level", "info"); s.Kind != metrictest.Counter || s.Unit != "" {
		t.Errorf("log_messages_total: %s, unit %q — want counter without unit", s.Kind, s.Unit)
	}
}

func TestLogMessagesTotal_SkipsFilteredRecords(t *testing.T) {
	before := metrictest.Collect(t)
	l := mustLogger(t, Config{Level: "warn"}, io.Discard)
	ctx := context.Background()
	l.Debug(ctx, "d")
	l.Info(ctx, "i")
	l.Warn(ctx, "w")

	assertDelta(t, before, map[string]float64{"debug": 0, "info": 0, "warn": 1})
}

func TestLogMessagesTotal_CountsSlogPath(t *testing.T) {
	before := metrictest.Collect(t)
	sl := mustSlog(t, Config{Level: "debug"}, io.Discard)
	sl.Info("via slog")
	sl.With("k", "v").WithGroup("g").Error("via slog group")

	assertDelta(t, before, map[string]float64{"info": 1, "error": 1})
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
