package outboxkit

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
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
	instance        attribute.KeyValue
	insertedTotal   metric.Int64Counter
	processedTotal  metric.Int64Counter
	deadTotal       metric.Int64Counter
	batchSize       metric.Int64Histogram
	batchDuration   metric.Float64Histogram
	publishDuration metric.Float64Histogram
}

func newOutboxInstruments(instance string) *outboxInstruments {
	m := otelkit.Meter("outboxkit")
	return &outboxInstruments{
		instance: attribute.String("instance", instance),
		insertedTotal: otelkit.Must(m.Int64Counter("outbox_events_inserted_total",
			metric.WithDescription("Events inserted into outbox"),
		)),
		processedTotal: otelkit.Must(m.Int64Counter("outbox_events_processed_total",
			metric.WithDescription("Events processed by outbox worker"),
		)),
		deadTotal: otelkit.Must(m.Int64Counter("outbox_events_dead_total",
			metric.WithDescription("Events marked dead after exhausting MaxAttempts"),
		)),
		batchSize: otelkit.Must(m.Int64Histogram("outbox_batch_size",
			metric.WithDescription("Batch size pulled per worker tick"),
		)),
		batchDuration: otelkit.Must(m.Float64Histogram("outbox_batch_duration_seconds",
			metric.WithDescription("Worker tick duration"),
			metric.WithUnit("s"),
		)),
		publishDuration: otelkit.Must(m.Float64Histogram("outbox_publish_duration_seconds",
			metric.WithDescription("Duration of the batch publish phase per worker tick"),
			metric.WithUnit("s"),
		)),
	}
}

// RegisterBacklogGauges регистрирует ObservableGauge'и
// `outbox_lag_seconds{instance,subject}` и `outbox_pending_count{instance,subject}`
// поверх BacklogReporter. Возвращает Registration — вызвать Unregister на shutdown.
func RegisterBacklogGauges(instance string, reporter BacklogReporter) (metric.Registration, error) {
	m := otelkit.Meter("outboxkit")
	lag := otelkit.Must(m.Float64ObservableGauge("outbox_lag_seconds",
		metric.WithDescription("Age of oldest unpublished event per subject"),
		metric.WithUnit("s"),
	))
	pending := otelkit.Must(m.Int64ObservableGauge("outbox_pending_count",
		metric.WithDescription("Number of unpublished events per subject"),
	))
	inst := attribute.String("instance", instance)
	return m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		backlog, err := reporter.Backlog(ctx)
		if err != nil {
			// Сбой снимка не должен валить экспорт метрик процесса.
			return nil
		}
		for _, b := range backlog {
			attrs := metric.WithAttributes(inst, attribute.String("subject", b.Subject))
			o.ObserveFloat64(lag, b.Oldest.Seconds(), attrs)
			o.ObserveInt64(pending, b.Pending, attrs)
		}
		return nil
	}, lag, pending)
}
