package outboxkit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
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

// Пустая очередь — серия инстанса с нулями, а не её отсутствие: алерт absent() не путает здоровый
// инстанс с пропавшим.
func TestRegisterBacklogGauges_EmptyQueueIsZero(t *testing.T) {
	collect := metricsSince(t)
	reg, err := outboxkit.RegisterBacklogGauges("orders", &fakeRepo{})
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })
	rm := collect()
	for _, name := range []string{"outbox_lag_seconds", "outbox_pending_count"} {
		if pts := rm.Find(name, "instance", "orders"); len(pts) != 1 || pts[0].Value != 0 {
			t.Errorf("%s of an empty queue: %v, want one zero point", name, pts)
		}
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
	// Первый сбой — на регистрации; сбор заказал следующий снимок, и он мог успеть упасть.
	if got := rm.Value("outbox_backlog_errors_total", "instance", "orders"); got < 1 {
		t.Fatalf("outbox_backlog_errors_total = %v, want the failed snapshot counted", got)
	}
}

// Регрессия ROBS-01: снимок очереди — запрос к БД — идёт вне пути сбора метрик. Пока хранилище
// не отвечает (занятый пул), сбор не ждёт его и отдаёт прошлый снимок: экспорт остальных метрик
// процесса не останавливается. Снимок доходит до gauge'ей следующим сбором, сбой снимка убирает
// серии и считается, Unregister прерывает идущий запрос.
func TestRegisterBacklogGauges_SnapshotIsOutsideCollection(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{backlog: []outboxkit.SubjectBacklog{{Subject: "order.created", Oldest: time.Second, Pending: 1}}}
	reg, err := outboxkit.RegisterBacklogGauges("orders", repo)
	if err != nil {
		t.Fatalf("RegisterBacklogGauges: %v", err)
	}
	pending := func() []metrictest.Series { return collect().Find("outbox_pending_count", "instance", "orders") }
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", what)
			}
		}
	}

	// Хранилище зависло: следующий снимок ждёт release (не дольше трёх секунд — столько сбор
	// метрик простоял бы, будь запрос в его пути).
	entered, release := make(chan struct{}, 8), make(chan struct{})
	cancelled := make(chan struct{}, 8)
	repo.mu.Lock()
	repo.backlog = []outboxkit.SubjectBacklog{{Subject: "order.created", Oldest: 2 * time.Second, Pending: 7}}
	repo.backlogHook = func(context.Context) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
	}
	repo.mu.Unlock()

	start := time.Now()
	pts := pending()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("collection waited %s for the storage: the snapshot is still in the collection path", elapsed)
	}
	if len(pts) != 1 || pts[0].Value != 1 {
		t.Fatalf("outbox_pending_count while the storage hangs: %v, want the last snapshot (1)", pts)
	}
	<-entered
	if pts := pending(); len(pts) != 1 || pts[0].Value != 1 {
		t.Fatalf("outbox_pending_count with a snapshot in flight: %v, want the last snapshot (1)", pts)
	}

	close(release)
	eventually("the new snapshot reaches the gauge", func() bool { p := pending(); return len(p) == 1 && p[0].Value == 7 })

	repo.mu.Lock()
	repo.backlogErr, repo.backlogHook = errors.New("pool is closed"), nil
	repo.mu.Unlock()
	eventually("a failed snapshot removes the series", func() bool { return len(pending()) == 0 })
	if got := collect().Value("outbox_backlog_errors_total", "instance", "orders"); got < 1 {
		t.Fatalf("outbox_backlog_errors_total = %v, want the failed snapshot counted", got)
	}

	repo.mu.Lock()
	repo.backlogErr, repo.backlogHook = nil, func(ctx context.Context) {
		entered <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
	}
	repo.mu.Unlock()
	for len(entered) > 0 {
		<-entered
	}
	eventually("a snapshot is requested by a collection", func() bool { pending(); return len(entered) > 0 })
	if err := reg.Unregister(); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Unregister left the snapshot in flight running")
	}
}
