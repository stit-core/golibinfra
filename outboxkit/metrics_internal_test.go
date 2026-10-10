package outboxkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

type backlogFunc func(context.Context) ([]SubjectBacklog, error)

func (f backlogFunc) Backlog(ctx context.Context) ([]SubjectBacklog, error) { return f(ctx) }

// Снимок очереди ограничен сроком — зависшая БД заканчивает его сбоем, а не держит вечно, — и
// outbox_backlog_errors_total считает только отказ хранилища: снимок, прерванный Unregister
// (ctx регистрации отменён), — не сбой, иначе каждая остановка сервиса давала бы ложный алерт.
func TestBacklogSnapshots_DeadlineAndInterruptedLoad(t *testing.T) {
	before := metrictest.Collect(t)
	failed := func() float64 {
		return metrictest.Collect(t).Sub(before).Value("outbox_backlog_errors_total", "instance", "snapshots")
	}
	snapshots := func(ctx context.Context, reporter BacklogReporter) *backlogSnapshots {
		return &backlogSnapshots{
			reporter: reporter, ctx: ctx, instance: attribute.String("instance", "snapshots"),
			errors: otelkit.Must(otelkit.Meter("outboxkit").Int64Counter("outbox_backlog_errors_total", metric.WithDescription("Failed backlog snapshots behind outbox_lag_seconds/outbox_pending_count"))),
		}
	}

	var left time.Duration
	snapshots(context.Background(), backlogFunc(func(ctx context.Context) ([]SubjectBacklog, error) {
		if deadline, ok := ctx.Deadline(); ok {
			left = time.Until(deadline)
		}
		return nil, nil
	})).load()
	// 5 с — срок снимка из доки кита.
	if left <= 0 || left > 5*time.Second {
		t.Fatalf("the snapshot ran with %s left to its deadline, want a deadline within 5s", left)
	}

	stopped, stop := context.WithCancel(context.Background())
	stop()
	snapshots(stopped, backlogFunc(func(ctx context.Context) ([]SubjectBacklog, error) { return nil, ctx.Err() })).load()
	if n := failed(); n != 0 {
		t.Fatalf("outbox_backlog_errors_total = %v after a snapshot interrupted by Unregister, want 0", n)
	}

	snapshots(context.Background(), backlogFunc(func(context.Context) ([]SubjectBacklog, error) { return nil, errors.New("pool is closed") })).load()
	if n := failed(); n != 1 {
		t.Fatalf("outbox_backlog_errors_total = %v after a failed snapshot, want 1", n)
	}
}
