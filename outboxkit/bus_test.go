package outboxkit_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

func TestBusPublishSerializesOnce(t *testing.T) {
	collect := collectMetrics(t)
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
	want := map[string]string{"instance": "orders", "subject": "order.created", "status": "ok"}
	if got := counterValue(collect(), "outbox_events_inserted_total", want); got != 1 {
		t.Fatalf("inserted counter = %d, want 1", got)
	}
}

func TestBusPublishInsertError(t *testing.T) {
	collect := collectMetrics(t)
	wantErr := errors.New("db down")
	bus := outboxkit.NewBus("orders", &fakeRepo{insertErr: wantErr})

	if err := bus.Publish(context.Background(), "order.created", struct{}{}); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	attrs := map[string]string{"instance": "orders", "status": "error"}
	if got := counterValue(collect(), "outbox_events_inserted_total", attrs); got != 1 {
		t.Fatalf("error counter = %d, want 1", got)
	}
}

func TestBusPublishMarshalError(t *testing.T) {
	collect := collectMetrics(t)
	repo := &fakeRepo{}
	bus := outboxkit.NewBus("orders", repo)

	if err := bus.Publish(context.Background(), "order.created", math.Inf(1)); err == nil {
		t.Fatal("expected marshal error")
	}
	if len(repo.inserted) != 0 {
		t.Fatal("unserializable payload must not reach the store")
	}
	attrs := map[string]string{"instance": "orders", "status": "marshal_error"}
	if got := counterValue(collect(), "outbox_events_inserted_total", attrs); got != 1 {
		t.Fatalf("marshal_error counter = %d, want 1", got)
	}
}
