package outboxkit_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

type insertedEvent struct {
	subject string
	payload json.RawMessage
}

// fakeRepo — Repository со всеми опциональными интерфейсами кита
// (BacklogReporter, DeadReviver, Maintainer) и управляемыми ошибками.
type fakeRepo struct {
	mu sync.Mutex

	unpublished []outboxkit.Message
	inserted    []insertedEvent
	marked      []outboxkit.MessageRef
	failures    []outboxkit.Failure
	limit       int
	listMode    outboxkit.PublishMode
	listDue     time.Time
	listHasDue  bool
	markCtxErr  error

	insertErr error
	listErr   error
	markErr   error
	recordErr error

	backlog    []outboxkit.SubjectBacklog
	backlogErr error

	revived      int64
	reviveFilter outboxkit.ReviveFilter
	reviveErr    error
}

func (r *fakeRepo) Insert(_ context.Context, subject string, payload json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.insertErr != nil {
		return r.insertErr
	}
	r.inserted = append(r.inserted, insertedEvent{subject: subject, payload: payload})
	return nil
}

func (r *fakeRepo) ListUnpublished(ctx context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limit = limit
	r.listMode = mode
	r.listDue, r.listHasDue = ctx.Deadline()
	if r.listErr != nil {
		return nil, r.listErr
	}
	if limit < len(r.unpublished) {
		return r.unpublished[:limit], nil
	}
	return r.unpublished, nil
}

func (r *fakeRepo) MarkPublished(ctx context.Context, refs []outboxkit.MessageRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markCtxErr = ctx.Err()
	if r.markErr != nil {
		return r.markErr
	}
	r.marked = append(r.marked, refs...)
	return nil
}

func (r *fakeRepo) RecordFailures(_ context.Context, failures []outboxkit.Failure) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr != nil {
		return r.recordErr
	}
	r.failures = append(r.failures, failures...)
	return nil
}

func (r *fakeRepo) Backlog(context.Context) ([]outboxkit.SubjectBacklog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backlogErr != nil {
		return nil, r.backlogErr
	}
	return r.backlog, nil
}

func (r *fakeRepo) ReviveDead(_ context.Context, f outboxkit.ReviveFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviveFilter = f
	if r.reviveErr != nil {
		return 0, r.reviveErr
	}
	return r.revived, nil
}

func (r *fakeRepo) MaintenanceJobs() []timerkit.Job {
	return []timerkit.Job{{
		Name:     "fake.maintenance",
		Interval: time.Hour,
		Run:      func(context.Context) error { return nil },
	}}
}

func (r *fakeRepo) markedIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, len(r.marked))
	for i, ref := range r.marked {
		ids[i] = ref.ID
	}
	return ids
}

func (r *fakeRepo) recordedFailures() []outboxkit.Failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outboxkit.Failure(nil), r.failures...)
}

// bareRepo — Repository без опциональных интерфейсов кита.
type bareRepo struct{ repo *fakeRepo }

func (b bareRepo) Insert(ctx context.Context, subject string, payload json.RawMessage) error {
	return b.repo.Insert(ctx, subject, payload)
}

func (b bareRepo) ListUnpublished(ctx context.Context, limit int, mode outboxkit.PublishMode) ([]outboxkit.Message, error) {
	return b.repo.ListUnpublished(ctx, limit, mode)
}

func (b bareRepo) MarkPublished(ctx context.Context, refs []outboxkit.MessageRef) error {
	return b.repo.MarkPublished(ctx, refs)
}

func (b bareRepo) RecordFailures(ctx context.Context, failures []outboxkit.Failure) error {
	return b.repo.RecordFailures(ctx, failures)
}

type fakeTransport struct {
	mu sync.Mutex

	published []string
	failOn    map[string]error
	batchErr  error
	deadline  bool
	// beforePublish вызывается до публикации каждого сообщения — точка,
	// в которой тест обрывает ctx воркера.
	beforePublish func()
}

func (t *fakeTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, t.deadline = ctx.Deadline()
	if t.beforePublish != nil {
		t.beforePublish()
	}
	if e, ok := t.failOn[m.Subject]; ok {
		return e
	}
	t.published = append(t.published, m.Subject)
	return nil
}

func (t *fakeTransport) PublishBatch(ctx context.Context, msgs []outboxkit.OutMsg) ([]error, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, t.deadline = ctx.Deadline()
	if t.beforePublish != nil {
		t.beforePublish()
	}
	if t.batchErr != nil {
		return nil, t.batchErr
	}
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		if e, ok := t.failOn[m.Subject]; ok {
			errs[i] = e
			continue
		}
		t.published = append(t.published, m.Subject)
	}
	return errs, nil
}

func (t *fakeTransport) publishedSubjects() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.published...)
}

// capturingBus — natskit.StreamBus, запоминающая опубликованное.
type capturingBus struct {
	mu     sync.Mutex
	batch  []natskit.StreamMsg
	single []natskit.PublishSpec
}

func (b *capturingBus) PublishStream(_ context.Context, subject string, payload []byte, opts ...natskit.PublishOption) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	spec := natskit.NewPublishSpec(opts...)
	b.single = append(b.single, spec)
	b.batch = append(b.batch, natskit.StreamMsg{
		Subject: subject, Payload: payload, MsgID: spec.MsgID, MetricSubject: spec.MetricSubject,
	})
	return nil
}

func (b *capturingBus) PublishStreamBatch(_ context.Context, msgs []natskit.StreamMsg) ([]error, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batch = append(b.batch, msgs...)
	return make([]error, len(msgs)), nil
}

func (b *capturingBus) SubscribeStream(context.Context, string, string, string, natskit.Handler) (natskit.Subscription, error) {
	return nil, nil
}

func (b *capturingBus) msgs() []natskit.StreamMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]natskit.StreamMsg(nil), b.batch...)
}

// collectMetrics ставит ManualReader глобальным провайдером; инструменты кита
// создаются в конструкторах, поэтому вызывается до них.
func collectMetrics(t *testing.T) func() metricdata.ResourceMetrics {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return func() metricdata.ResourceMetrics {
		t.Helper()
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		return rm
	}
}

// counterValue — сумма точек счётчика name с указанными атрибутами.
func counterValue(rm metricdata.ResourceMetrics, name string, attrs map[string]string) int64 {
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if matchAttrs(dp.Attributes, attrs) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// gaugePoints — число точек ObservableGauge name.
func gaugePoints(rm metricdata.ResourceMetrics, name string) int {
	n := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Gauge[float64]:
				n += len(d.DataPoints)
			case metricdata.Gauge[int64]:
				n += len(d.DataPoints)
			}
		}
	}
	return n
}

func histogramBounds(rm metricdata.ResourceMetrics, name string) []float64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && len(h.DataPoints) > 0 {
				return h.DataPoints[0].Bounds
			}
		}
	}
	return nil
}

func matchAttrs(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}
