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
	// Subject — subject событий снимка.
	Subject string
	// Oldest — возраст самого старого неопубликованного события.
	Oldest time.Duration
	// Pending — число неопубликованных (не-dead) событий.
	Pending int64
}

// BacklogReporter — опциональный интерфейс Repository: реализующие его store'ы
// (см. outboxkit/pgstore) экспонируют backlog по subject'ам. Через
// RegisterBacklogGauges это становится `outbox_lag_seconds` и
// `outbox_pending_count` (ObservableGauge).
type BacklogReporter interface {
	Backlog(ctx context.Context) ([]SubjectBacklog, error)
}

type outboxInstruments struct {
	instance attribute.KeyValue
	// metricSubject — WithMetricSubject инстанса; nil — label `subject` нет.
	metricSubject   func(subject string) string
	insertedTotal   metric.Int64Counter
	processedTotal  metric.Int64Counter
	deadTotal       metric.Int64Counter
	revivedTotal    metric.Int64Counter
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
		revivedTotal: otelkit.Must(m.Int64Counter("outbox_revived_total",
			metric.WithDescription("Dead events returned to the queue by ReviveDead"),
		)),
		batchSize: otelkit.Must(m.Int64Histogram("outbox_batch_size",
			metric.WithDescription("Batch size pulled per worker tick"),
		)),
		batchDuration: otelkit.Must(m.Float64Histogram("outbox_batch_duration_seconds",
			metric.WithDescription("Worker tick duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
		publishDuration: otelkit.Must(m.Float64Histogram("outbox_publish_duration_seconds",
			metric.WithDescription("Duration of the batch publish phase per worker tick"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
	}
}

// subject — label `subject` сигнала: шаблон из WithMetricSubject; без него label нет — id в
// subject размножил бы серии.
func (in *outboxInstruments) subject(subject string) []attribute.KeyValue {
	if in.metricSubject == nil {
		return nil
	}
	return []attribute.KeyValue{attribute.String("subject", in.metricSubject(subject))}
}

// template — шаблон subject'а для сигналов брокера; пусто без WithMetricSubject.
func (in *outboxInstruments) template(subject string) string {
	if in.metricSubject == nil {
		return ""
	}
	return in.metricSubject(subject)
}

// RegisterBacklogGauges регистрирует ObservableGauge'и `outbox_lag_seconds{instance}` и
// `outbox_pending_count{instance}` поверх BacklogReporter: самое старое событие и число
// неопубликованных по всей очереди инстанса. Возвращает Registration — вызвать Unregister на
// shutdown.
func RegisterBacklogGauges(instance string, reporter BacklogReporter) (metric.Registration, error) {
	return registerBacklogGauges(instance, reporter, nil)
}

// registerBacklogGauges: fold — WithMetricSubject инстанса, снимки сворачиваются по шаблону
// (самое старое, сумма); nil — одна серия на инстанс.
func registerBacklogGauges(instance string, reporter BacklogReporter, fold func(string) string) (metric.Registration, error) {
	m := otelkit.Meter("outboxkit")
	lag := otelkit.Must(m.Float64ObservableGauge("outbox_lag_seconds",
		metric.WithDescription("Age of oldest unpublished event"),
		metric.WithUnit("s"),
	))
	pending := otelkit.Must(m.Int64ObservableGauge("outbox_pending_count",
		metric.WithDescription("Number of unpublished events"),
	))
	backlogErrors := otelkit.Must(m.Int64Counter("outbox_backlog_errors_total",
		metric.WithDescription("Failed backlog snapshots behind outbox_lag_seconds/outbox_pending_count"),
	))
	inst := attribute.String("instance", instance)
	return m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		backlog, err := reporter.Backlog(ctx)
		if err != nil {
			// Сбой снимка не должен валить экспорт метрик процесса, но и
			// пропадать молча: без счётчика алерт на lag не отличит «нет
			// отставания» от «снимок не берётся».
			backlogErrors.Add(ctx, 1, metric.WithAttributes(inst))
			return nil //nolint:nilerr // сбой снимка учтён метрикой, экспорт процесса не валим
		}
		type series struct {
			oldest  time.Duration
			pending int64
		}
		folded := map[string]series{}
		for _, b := range backlog {
			key := ""
			if fold != nil {
				key = fold(b.Subject)
			}
			s := folded[key]
			s.oldest = max(s.oldest, b.Oldest)
			s.pending += b.Pending
			folded[key] = s
		}
		for key, s := range folded {
			kv := []attribute.KeyValue{inst}
			if fold != nil {
				kv = append(kv, attribute.String("subject", key))
			}
			o.ObserveFloat64(lag, s.oldest.Seconds(), metric.WithAttributes(kv...))
			o.ObserveInt64(pending, s.pending, metric.WithAttributes(kv...))
		}
		return nil
	}, lag, pending)
}
