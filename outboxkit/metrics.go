package outboxkit

import (
	"context"
	"sync"
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
// `outbox_pending_count` (ObservableGauge). Backlog обязан уважать ctx: срок снимка действует
// через него, и реализация, которая ctx не читает, на зависшем хранилище оставляет gauge'и на
// последнем значении — следующий снимок не начнётся, пока идёт этот.
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

// backlogTimeout — срок одного снимка очереди: при занятом пуле или зависшей БД запрос
// заканчивается сбоем снимка, а не ждёт вечно.
const backlogTimeout = 5 * time.Second

// backlogSnapshots держит последний снимок очереди. Запрос к хранилищу идёт вне пути сбора
// метрик: callback отдаёт готовый снимок и заказывает следующий, поэтому занятый пул не
// останавливает экспорт остальных метрик процесса. После сбоя снимка серий нет до следующего
// удачного — «нет данных», а не устаревшее «очередь пуста».
type backlogSnapshots struct {
	reporter BacklogReporter
	errors   metric.Int64Counter
	instance attribute.KeyValue
	// ctx живёт от регистрации до Unregister.
	ctx context.Context

	mu      sync.Mutex
	rows    []SubjectBacklog
	ok      bool
	loading bool
}

func (b *backlogSnapshots) load() {
	ctx, cancel := context.WithTimeout(b.ctx, backlogTimeout)
	defer cancel()
	rows, err := b.reporter.Backlog(ctx)
	b.mu.Lock()
	b.rows, b.ok, b.loading = rows, err == nil, false
	b.mu.Unlock()
	// Сбой снимка не должен пропадать молча: без счётчика алерт на lag не отличит «нет
	// отставания» от «снимок не берётся». Снимок, прерванный Unregister, — не сбой.
	if err != nil && b.ctx.Err() == nil {
		b.errors.Add(ctx, 1, metric.WithAttributes(b.instance))
	}
}

// last отдаёт последний снимок и заказывает следующий, если он ещё не идёт.
func (b *backlogSnapshots) last() ([]SubjectBacklog, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loading && b.ctx.Err() == nil {
		b.loading = true
		go b.load()
	}
	return b.rows, b.ok
}

// backlogRegistration — регистрация callback'а вместе со снимками: Unregister прерывает идущий.
type backlogRegistration struct {
	metric.Registration
	stop context.CancelFunc
}

func (r backlogRegistration) Unregister() error {
	r.stop()
	return r.Registration.Unregister()
}

// RegisterBacklogGauges регистрирует ObservableGauge'и `outbox_lag_seconds{instance}` и
// `outbox_pending_count{instance}` поверх BacklogReporter: самое старое событие и число
// неопубликованных по всей очереди инстанса. Первый снимок берётся здесь же (не дольше
// backlogTimeout), следующие — вне сбора метрик: gauge'и отдают снимок, заказанный прошлым
// сбором. Возвращает Registration — вызвать Unregister на shutdown.
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
	ctx, stop := context.WithCancel(context.Background())
	snapshots := &backlogSnapshots{reporter: reporter, errors: backlogErrors, instance: inst, ctx: ctx, loading: true}
	snapshots.load()
	reg, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		backlog, ok := snapshots.last()
		if !ok {
			return nil
		}
		type series struct {
			oldest  time.Duration
			pending int64
		}
		folded := map[string]series{}
		if fold == nil {
			// Серия инстанса есть и у пустой очереди — 0, а не «нет данных» для absent().
			folded[""] = series{}
		}
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
	if err != nil {
		stop()
		return nil, err
	}
	return backlogRegistration{Registration: reg, stop: stop}, nil
}
