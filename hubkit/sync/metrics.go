package sync

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
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
	m := otelkit.Meter("hubkit/sync")
	records := otelkit.Must(m.Int64Counter(
		"hub_sync_records_total",
		metric.WithDescription("Records upserted into local projection by hub sync"),
	))
	errs := otelkit.Must(m.Int64Counter(
		"hub_sync_errors_total",
		metric.WithDescription("Failed hub sync ticks"),
	))
	lastSuccess := otelkit.Must(m.Float64ObservableGauge(
		"hub_sync_last_success_timestamp_seconds",
		metric.WithDescription("Unix time of last successful hub sync catch-up"),
		metric.WithUnit("s"),
	))
	lag := otelkit.Must(m.Float64ObservableGauge(
		"hub_sync_lag_seconds",
		metric.WithDescription("Time since last successful hub sync catch-up"),
		metric.WithUnit("s"),
	))
	cursor := otelkit.Must(m.Int64ObservableGauge(
		"hub_sync_cursor_seq",
		metric.WithDescription("Current hub sync cursor (updated_seq) per resource"),
		metric.WithUnit("{seq}"),
	))
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
// atomic-полей движка. Тихо no-op без MeterProvider. Курсор и время успеха
// наблюдаются только после первого чтения/успеха: нулевая точка до них
// неотличима от «фид с начала» и врёт на дашборде. Регистрацию возвращаем
// движку: замыкание держит его живым, пока она не снята (Engine.Close).
func (s *syncInstruments) registerObservables(e *Engine) metric.Registration {
	return otelkit.Must(otelkit.Meter("hubkit/sync").RegisterCallback(func(_ context.Context, o metric.Observer) error {
		if e.cursorKnown.Load() {
			o.ObserveInt64(s.cursorGauge, e.cursorValue.Load(), s.attrs)
		}
		if last := e.lastSuccessUnix.Load(); last > 0 {
			o.ObserveFloat64(s.lastSuccessTS, float64(last), s.attrs)
			o.ObserveFloat64(s.lagSeconds, e.clock.Since(time.Unix(last, 0)).Seconds(), s.attrs)
		}
		return nil
	}, s.lastSuccessTS, s.lagSeconds, s.cursorGauge))
}
