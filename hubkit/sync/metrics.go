package sync

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type syncInstruments struct {
	resource      string
	recordsTotal  metric.Int64Counter
	errorsTotal   metric.Int64Counter
	lastSuccessTS metric.Float64ObservableGauge
	lagSeconds    metric.Float64ObservableGauge
	cursorGauge   metric.Int64ObservableGauge
	attrs         metric.MeasurementOption
}

func newSyncInstruments(resource string) *syncInstruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/hubkit/sync")
	records, _ := m.Int64Counter(
		"hub_sync_records_total",
		metric.WithDescription("Records upserted into local projection by hub sync"),
	)
	errs, _ := m.Int64Counter(
		"hub_sync_errors_total",
		metric.WithDescription("Failed hub sync ticks"),
	)
	lastSuccess, _ := m.Float64ObservableGauge(
		"hub_sync_last_success_timestamp_seconds",
		metric.WithDescription("Unix time of last successful hub sync catch-up"),
		metric.WithUnit("s"),
	)
	lag, _ := m.Float64ObservableGauge(
		"hub_sync_lag_seconds",
		metric.WithDescription("Time since last successful hub sync catch-up"),
		metric.WithUnit("s"),
	)
	cursor, _ := m.Int64ObservableGauge(
		"hub_sync_cursor",
		metric.WithDescription("Current hub sync cursor (updated_seq) per resource"),
	)
	return &syncInstruments{
		resource:      resource,
		recordsTotal:  records,
		errorsTotal:   errs,
		lastSuccessTS: lastSuccess,
		lagSeconds:    lag,
		cursorGauge:   cursor,
		attrs:         metric.WithAttributes(attribute.String("resource", resource)),
	}
}

func (s *syncInstruments) recordRecords(ctx context.Context, n int) {
	s.recordsTotal.Add(ctx, int64(n), s.attrs)
}

func (s *syncInstruments) recordError(ctx context.Context) {
	s.errorsTotal.Add(ctx, 1, s.attrs)
}

// registerObservables вешает callback на last-success / lag / cursor поверх
// atomic-полей движка. Тихо no-op без MeterProvider.
func (s *syncInstruments) registerObservables(e *Engine) {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/hubkit/sync")
	_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		last := e.lastSuccessUnix.Load()
		o.ObserveInt64(s.cursorGauge, e.cursorValue.Load(), s.attrs)
		if last > 0 {
			o.ObserveFloat64(s.lastSuccessTS, float64(last), s.attrs)
			o.ObserveFloat64(s.lagSeconds, time.Since(time.Unix(last, 0)).Seconds(), s.attrs)
		}
		return nil
	}, s.lastSuccessTS, s.lagSeconds, s.cursorGauge)
}
