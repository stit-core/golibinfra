package outboxkit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// SubjectBacklog — снимок очереди неопубликованных событий по subject'у:
// возраст самого старого (Oldest) и количество (Pending).
type SubjectBacklog struct {
	Subject string
	Oldest  time.Duration
	Pending int64
}

// BacklogReporter — optional интерфейс для Repository: реализующие его store'ы
// (см. outboxkit/pgstore) экспонируют backlog по subject'ам. Через
// RegisterBacklogGauges это становится `outbox_lag_seconds` и
// `outbox_pending_count` (ObservableGauge).
type BacklogReporter interface {
	Backlog(ctx context.Context) ([]SubjectBacklog, error)
}

type outboxInstruments struct {
	insertedTotal   metric.Int64Counter
	processedTotal  metric.Int64Counter
	deadTotal       metric.Int64Counter
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
	dead, _ := m.Int64Counter("outbox_events_dead_total",
		metric.WithDescription("Events marked dead after exhausting MaxAttempts"),
	)
	batchSize, _ := m.Int64Histogram("outbox_batch_size",
		metric.WithDescription("Batch size pulled per worker tick"),
	)
	batchDur, _ := m.Float64Histogram("outbox_batch_duration_seconds",
		metric.WithDescription("Worker tick duration"),
		metric.WithUnit("s"),
	)
	pubDur, _ := m.Float64Histogram("outbox_publish_duration_seconds",
		metric.WithDescription("Duration of the batch publish phase per worker tick"),
		metric.WithUnit("s"),
	)
	return &outboxInstruments{
		insertedTotal:   inserted,
		processedTotal:  processed,
		deadTotal:       dead,
		batchSize:       batchSize,
		batchDuration:   batchDur,
		publishDuration: pubDur,
	}
}

// RegisterBacklogGauges регистрирует ObservableGauge'и `outbox_lag_seconds{subject}`
// и `outbox_pending_count{subject}` поверх BacklogReporter. Возвращает
// Registration — вызвать Unregister на shutdown.
func RegisterBacklogGauges(reporter BacklogReporter) (metric.Registration, error) {
	m := otel.Meter("git.stit.tech/stit-core/golibinfra/outboxkit")
	lag, err := m.Float64ObservableGauge("outbox_lag_seconds",
		metric.WithDescription("Age of oldest unpublished event per subject"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	pending, err := m.Int64ObservableGauge("outbox_pending_count",
		metric.WithDescription("Number of unpublished events per subject"),
	)
	if err != nil {
		return nil, err
	}
	return m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		backlog, err := reporter.Backlog(ctx)
		if err != nil {
			// Тихо пропускаем — observability не должна валить процесс.
			return nil
		}
		for _, b := range backlog {
			attrs := metric.WithAttributes(attribute.String("subject", b.Subject))
			o.ObserveFloat64(lag, b.Oldest.Seconds(), attrs)
			o.ObserveInt64(pending, b.Pending, attrs)
		}
		return nil
	}, lag, pending)
}
