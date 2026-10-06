package outboxkit_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/testkit/pgtest"
)

func TestBusPublishSerializesOnce(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{}
	bus := outboxkit.NewBus("orders", repo)

	if err := bus.Publish(context.Background(), "order.created", map[string]int{"id": 7}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
	// Хранилище получает готовый JSON — сериализация живёт в Bus.
	if got := string(repo.inserted[0].payload); got != `{"id":7}` {
		t.Fatalf("payload = %s, want {\"id\":7}", got)
	}
	rm := collect()
	if got := rm.Value("outbox_events_inserted_total", "instance", "orders", "status", "ok"); got != 1 {
		t.Fatalf("inserted counter = %v, want 1", got)
	}
	// Без WithMetricSubject label subject нет: id в subject размножил бы серии (KO11).
	for _, s := range rm.Find("outbox_events_inserted_total", "instance", "orders") {
		if _, ok := s.Labels["subject"]; ok {
			t.Fatalf("series %v carries subject without WithMetricSubject", s.Labels)
		}
	}
}

func TestBusPublishInsertError(t *testing.T) {
	collect := metricsSince(t)
	wantErr := errors.New("db down")
	bus := outboxkit.NewBus("orders", &fakeRepo{insertErr: wantErr})

	if err := bus.Publish(context.Background(), "order.created", struct{}{}); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	attrs := []string{"instance", "orders", "status", "error"}
	if got := collect().Value("outbox_events_inserted_total", attrs...); got != 1 {
		t.Fatalf("error counter = %v, want 1", got)
	}
}

func TestBusPublishMarshalError(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{}
	bus := outboxkit.NewBus("orders", repo)

	if err := bus.Publish(context.Background(), "order.created", math.Inf(1)); err == nil {
		t.Fatal("expected marshal error")
	}
	if len(repo.inserted) != 0 {
		t.Fatal("unserializable payload must not reach the store")
	}
	attrs := []string{"instance", "orders", "status", "marshal_error"}
	if got := collect().Value("outbox_events_inserted_total", attrs...); got != 1 {
		t.Fatalf("marshal_error counter = %v, want 1", got)
	}
}

// JSON больше предела отвергается в транзакции домена: строку, которую брокер не примет,
// воркер ретраил бы весь бюджет попыток, держа ordered-очередь.
func TestBusPublishRejectsOversizedPayload(t *testing.T) {
	collect := metricsSince(t)
	repo := &fakeRepo{}
	bus := outboxkit.NewBus("orders", repo)

	big := strings.Repeat("x", outboxkit.DefaultMaxPayloadBytes)
	if err := bus.Publish(context.Background(), "order.created", big); !errors.Is(err, outboxkit.ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatal("oversized payload must not reach the store")
	}
	attrs := []string{"instance", "orders", "status", "payload_too_large"}
	if got := collect().Value("outbox_events_inserted_total", attrs...); got != 1 {
		t.Fatalf("payload_too_large counter = %v, want 1", got)
	}
}

// Предел инстанса — OUTBOX_MAX_PAYLOAD_BYTES: JSON на пределе проходит, на байт больше — нет.
func TestOutbox_PayloadLimitFromConfig(t *testing.T) {
	repo := &fakeRepo{}
	o := mustNew(t, "orders", repo, &fakeTransport{}, &pgtest.Recorder{},
		valid(outboxkit.Config{MaxPayloadBytes: 8}),
		logkit.Nop())
	ctx := context.Background()

	if err := o.Publisher().Publish(ctx, "a", "123456"); err != nil {
		t.Fatalf("8-byte JSON at the limit: %v", err)
	}
	if err := o.Publisher().Publish(ctx, "a", "1234567"); !errors.Is(err, outboxkit.ErrPayloadTooLarge) {
		t.Fatalf("9-byte JSON: err = %v, want ErrPayloadTooLarge", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted = %d, want 1", len(repo.inserted))
	}
}
