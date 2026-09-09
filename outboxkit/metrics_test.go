package outboxkit_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

func TestRegisterBacklogGauges_ObservesSnapshot(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{backlog: []outboxkit.SubjectBacklog{
		{Subject: "order.created", Oldest: 3 * time.Second, Pending: 5},
		{Subject: "order.paid", Oldest: time.Second, Pending: 1},
	}}

	reg, err := outboxkit.RegisterBacklogGauges("orders", repo)
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	rm := collect()
	if n := gaugePoints(rm, "outbox_lag_seconds"); n != 2 {
		t.Fatalf("outbox_lag_seconds points = %d, want 2", n)
	}
	if n := gaugePoints(rm, "outbox_pending_count"); n != 2 {
		t.Fatalf("outbox_pending_count points = %d, want 2", n)
	}

	if err := reg.Unregister(); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if n := gaugePoints(collect(), "outbox_lag_seconds"); n != 0 {
		t.Fatalf("gauges observed after Unregister: %d points", n)
	}
}

// Сбой снимка не валит экспорт, но и не исчезает молча: иначе алерт на lag не
// отличит «нет отставания» от «снимок не берётся».
func TestRegisterBacklogGauges_ErrorCounted(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{backlogErr: errors.New("permission denied")}

	reg, err := outboxkit.RegisterBacklogGauges("orders", repo)
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })

	rm := collect()
	if n := gaugePoints(rm, "outbox_pending_count"); n != 0 {
		t.Fatalf("failed snapshot produced %d points", n)
	}
	if got := counterValue(rm, "outbox_backlog_errors_total", map[string]string{"instance": "orders"}); got != 1 {
		t.Fatalf("outbox_backlog_errors_total = %d, want 1", got)
	}
}
