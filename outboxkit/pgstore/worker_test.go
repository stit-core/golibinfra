package pgstore_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// barrierTransport пускает публикацию только когда в неё вошли оба воркера —
// так тики гарантированно пересекаются во времени.
type barrierTransport struct {
	entered atomic.Int32
	ready   chan struct{}
	fail    bool

	mu  sync.Mutex
	ids []int64
}

func newBarrierTransport(fail bool) *barrierTransport {
	return &barrierTransport{ready: make(chan struct{}), fail: fail}
}

func (b *barrierTransport) record(msgs []outboxkit.OutMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range msgs {
		b.ids = append(b.ids, m.ID)
	}
}

func (b *barrierTransport) wait(ctx context.Context) error {
	if b.entered.Add(1) == 2 {
		close(b.ready)
	}
	select {
	case <-b.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *barrierTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error {
	b.record([]outboxkit.OutMsg{m})
	if err := b.wait(ctx); err != nil {
		return err
	}
	if b.fail {
		return errors.New("nak")
	}
	return nil
}

func (b *barrierTransport) PublishBatch(ctx context.Context, msgs []outboxkit.OutMsg) ([]error, error) {
	b.record(msgs)
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	errs := make([]error, len(msgs))
	if b.fail {
		for i := range errs {
			errs[i] = errors.New("nak")
		}
	}
	return errs, nil
}

func (b *barrierTransport) published() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.ids...)
}

func insertN(t *testing.T, ctx context.Context, s *pgstore.Store, n int) {
	t.Helper()
	for i := range n {
		if err := s.Insert(ctx, "evt", payload(t, sample{N: i}), outboxkit.DefaultMaxPayloadBytes); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
}

func runTwoWorkers(t *testing.T, ctx context.Context, w1, w2 *outboxkit.Worker) (err1, err2 error) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); err1 = w1.Run(ctx) }()
	go func() { defer wg.Done(); err2 = w2.Run(ctx) }()
	wg.Wait()
	return err1, err2
}

func rowStats(t *testing.T, ctx context.Context, db *pgkit.DB) (published, attempts int) {
	t.Helper()
	if err := db.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE published_at IS NOT NULL), coalesce(sum(attempts), 0) FROM outbox`,
	).Scan(&published, &attempts); err != nil {
		t.Fatalf("row stats: %v", err)
	}
	return published, attempts
}

// Два batch-воркера с пересекающимися тиками делят очередь без дублей.
func TestWorkers_ConcurrentBatch_NoDuplicates(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	insertN(t, ctx, s, 10)

	transport := newBarrierTransport(false)
	cfg := outboxkit.Config{BatchSize: 5, Interval: time.Second, PublishMode: outboxkit.PublishBatch, TickTimeout: 30 * time.Second, MaxAttempts: 25, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
	w1 := mustWorker(t, "w1", s, transport, tr, cfg)
	w2 := mustWorker(t, "w2", s, transport, tr, cfg)

	if err1, err2 := runTwoWorkers(t, ctx, w1, w2); err1 != nil || err2 != nil {
		t.Fatalf("Run: w1=%v w2=%v", err1, err2)
	}

	ids := transport.published()
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("id %d published twice: %v", id, ids)
		}
		seen[id] = true
	}
	if len(ids) != 10 {
		t.Fatalf("published %d ids, want 10: %v", len(ids), ids)
	}
	if published, _ := rowStats(t, ctx, db); published != 10 {
		t.Fatalf("published rows = %d, want 10", published)
	}
}

// Неудача считается один раз на строку, даже когда тики двух воркеров пересеклись.
func TestWorkers_ConcurrentBatch_AttemptsCountedOnce(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	insertN(t, ctx, s, 10)

	transport := newBarrierTransport(true)
	cfg := outboxkit.Config{BatchSize: 5, Interval: time.Second, PublishMode: outboxkit.PublishBatch, TickTimeout: 30 * time.Second, MaxAttempts: 25, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
	w1 := mustWorker(t, "w1", s, transport, tr, cfg)
	w2 := mustWorker(t, "w2", s, transport, tr, cfg)

	if err1, err2 := runTwoWorkers(t, ctx, w1, w2); err1 == nil || err2 == nil {
		t.Fatalf("expected publish errors, got w1=%v w2=%v", err1, err2)
	}
	published, attempts := rowStats(t, ctx, db)
	if published != 0 {
		t.Fatalf("published rows = %d, want 0", published)
	}
	if attempts != 10 {
		t.Fatalf("sum(attempts) = %d, want 10 (по одной попытке на строку)", attempts)
	}
}

// Ordered: пока первый воркер публикует голову очереди, второй пропускает тик;
// итоговый порядок публикации — порядок вставки, без дублей.
func TestWorkers_Ordered_SingleDrainer(t *testing.T) {
	db, tr, ctx := startStore(t)
	s := pgstore.NewStore(db)
	insertN(t, ctx, s, 6)

	transport := &gateTransport{gate: make(chan struct{}), inside: make(chan struct{}, 1)}
	cfg := outboxkit.Config{BatchSize: 3, Interval: time.Second, PublishMode: outboxkit.PublishOrdered, TickTimeout: 30 * time.Second, MaxAttempts: 25, MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes}
	w1 := mustWorker(t, "w1", s, transport, tr, cfg)
	w2 := mustWorker(t, "w2", s, transport, tr, cfg)

	done := make(chan error, 1)
	go func() { done <- w1.Run(ctx) }()
	<-transport.inside // w1 держит голову очереди внутри публикации

	if err := w2.Run(ctx); err != nil {
		t.Fatalf("w2 while head is locked: %v (want silent skip)", err)
	}
	if n := len(transport.published()); n != 1 {
		t.Fatalf("w2 published while head locked: %d msgs seen, want 1 (только w1)", n)
	}

	close(transport.gate)
	if err := <-done; err != nil {
		t.Fatalf("w1: %v", err)
	}
	if err := w2.Run(ctx); err != nil {
		t.Fatalf("w2 second tick: %v", err)
	}

	ids := transport.published()
	if len(ids) != 6 {
		t.Fatalf("published %d, want 6: %v", len(ids), ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("order violated: %v", ids)
		}
	}
	if published, _ := rowStats(t, ctx, db); published != 6 {
		t.Fatalf("published rows = %d, want 6", published)
	}
}

// gateTransport блокирует первую публикацию до открытия gate и сигналит inside.
type gateTransport struct {
	gate   chan struct{}
	inside chan struct{}
	once   sync.Once

	mu  sync.Mutex
	ids []int64
}

func (g *gateTransport) Publish(ctx context.Context, m outboxkit.OutMsg) error {
	g.mu.Lock()
	g.ids = append(g.ids, m.ID)
	g.mu.Unlock()
	g.once.Do(func() {
		g.inside <- struct{}{}
		select {
		case <-g.gate:
		case <-ctx.Done():
		}
	})
	return ctx.Err()
}

func (g *gateTransport) PublishBatch(context.Context, []outboxkit.OutMsg) ([]error, error) {
	return nil, errors.New("ordered test uses Publish")
}

func (g *gateTransport) published() []int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int64(nil), g.ids...)
}

func mustWorker(t *testing.T, name string, repo outboxkit.Repository, transport outboxkit.Transport, tr pgkit.Transactor, cfg outboxkit.Config) *outboxkit.Worker {
	t.Helper()
	w, err := outboxkit.NewWorker(name, cfg, repo, transport, tr, logkit.Nop())
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}
