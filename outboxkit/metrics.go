package outboxkit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// LagReporter — optional интерфейс для Repository: реализующие его
// store'ы (см. outboxkit/pgstore) экспонируют отставание самого старого
// неопубликованного события для каждого subject'а. Через RegisterLagGauge
// эти данные подключаются как ObservableGauge.
type LagReporter interface {
	LagBySubject(ctx context.Context) (map[string]time.Duration, error)
}

type outboxInstruments struct {
	insertedTotal   metric.Int64Counter
	processedTotal  metric.Int64Counter
	batchSize       metric.Int64Histogram
	batchDuration   metric.Float64Histogram
	publishDuration metric.Float64Histogram
}

func newOutboxInstruments() *outboxInstruments {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/outboxkit")
	inserted, _ := m.Int64Counter("outbox_events_inserted_total",
		metric.WithDescription("Events inserted into outbox"),
	)
	processed, _ := m.Int64Counter("outbox_events_processed_total",
		metric.WithDescription("Events processed by outbox worker"),
	)
	batchSize, _ := m.Int64Histogram("outbox_batch_size",
		metric.WithDescription("Batch size pulled per worker tick"),
	)
	batchDur, _ := m.Float64Histogram("outbox_batch_duration_seconds",
		metric.WithDescription("Worker tick duration"),
		metric.WithUnit("s"),
	)
	pubDur, _ := m.Float64Histogram("outbox_publish_duration_seconds",
		metric.WithDescription("Time to publish + mark single event"),
		metric.WithUnit("s"),
	)
	return &outboxInstruments{
		insertedTotal:   inserted,
		processedTotal:  processed,
		batchSize:       batchSize,
		batchDuration:   batchDur,
		publishDuration: pubDur,
	}
}

// RegisterLagGauge регистрирует ObservableGauge `outbox_lag_seconds{subject}`
// поверх LagReporter. Возвращает Registration — вызвать Unregister на shutdown.
func RegisterLagGauge(reporter LagReporter) (metric.Registration, error) {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/outboxkit")
	gauge, err := m.Float64ObservableGauge("outbox_lag_seconds",
		metric.WithDescription("Age of oldest unpublished event per subject"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	return m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		lags, err := reporter.LagBySubject(ctx)
		if err != nil {
			// Тихо пропускаем — observability не должна валить процесс.
			return nil
		}
		for subj, lag := range lags {
			o.ObserveFloat64(gauge, lag.Seconds(),
				metric.WithAttributes(attribute.String("subject", subj)),
			)
		}
		return nil
	}, gauge)
}
