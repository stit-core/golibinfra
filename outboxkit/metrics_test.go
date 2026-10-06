package outboxkit_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

func TestRegisterBacklogGauges_ObservesSnapshot(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{backlog: []outboxkit.SubjectBacklog{
		{Subject: "order.created", Oldest: 3 * time.Second, Pending: 5},
		{Subject: "order.paid", Oldest: time.Second, Pending: 1},
	}}

	reg, err := outboxkit.RegisterBacklogGauges("orders", repo)
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	// Одна серия на инстанс: самое старое и сумма по всем subject'ам — id в subject не размножает
	// серии (KO21).
	rm := collect()
	if n := len(rm.Find("outbox_lag_seconds", "instance", "orders")); n != 1 {
		t.Fatalf("outbox_lag_seconds points = %d, want 1", n)
	}
	if got := rm.Value("outbox_lag_seconds", "instance", "orders"); got != 3 {
		t.Fatalf("outbox_lag_seconds = %v, want 3 (the oldest)", got)
	}
	if got := rm.Value("outbox_pending_count", "instance", "orders"); got != 6 {
		t.Fatalf("outbox_pending_count = %v, want 6 (the sum)", got)
	}

	if err := reg.Unregister(); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if n := len(collect().Find("outbox_lag_seconds", "instance", "orders")); n != 0 {
		t.Fatalf("gauges observed after Unregister: %d points", n)
	}
}

// Сбой снимка не валит экспорт, но и не исчезает молча: иначе алерт на lag не
// отличит «нет отставания» от «снимок не берётся».
func TestRegisterBacklogGauges_ErrorCounted(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{backlogErr: errors.New("permission denied")}

	reg, err := outboxkit.RegisterBacklogGauges("orders", repo)
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })

	rm := collect()
	if n := len(rm.Find("outbox_pending_count", "instance", "orders")); n != 0 {
		t.Fatalf("failed snapshot produced %d points", n)
	}
	if got := rm.Value("outbox_backlog_errors_total", "instance", "orders"); got != 1 {
		t.Fatalf("outbox_backlog_errors_total = %v, want 1", got)
	}
}
