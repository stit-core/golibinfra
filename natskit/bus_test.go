package natskit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestBus_QueueGroupLoadBalances(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()

	const n = 12
	got := make(chan struct{}, n*2)
	h := func(_ context.Context, _ []byte) error { got <- struct{}{}; return nil }
	for i := 0; i < 2; i++ {
		if _, err := bus.SubscribeQueue(ctx, "work.items", "workers", h); err != nil {
			t.Fatalf("subscribe queue: %v", err)
		}
	}

	for i := 0; i < n; i++ {
		if err := bus.Publish(ctx, "work.items", i); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	count := 0
	deadline := time.After(5 * time.Second)
	for count < n {
		select {
		case <-got:
			count++
		case <-deadline:
			t.Fatalf("got %d/%d", count, n)
		}
	}
	// queue-group: каждое сообщение ровно одному подписчику → не больше n.
	expectSilence(t, got, 500*time.Millisecond, "duplicate delivery")
}

func TestBus_PublishSubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := "test.roundtrip"

	got := make(chan []byte, 1)
	if _, err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		got <- payload
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(ctx, subject, map[string]string{"hello": "world"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if payload := recv(t, got, 3*time.Second, "message"); string(payload) != `{"hello":"world"}` {
		t.Errorf("unexpected payload: %s", payload)
	}
}

func TestBus_PublishSubscribeJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := "test.json"

	type Msg struct {
		Value int `json:"value"`
	}

	got := make(chan Msg, 1)
	if _, err := natskit.SubscribeJSON(bus, ctx, subject, func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe json: %v", err)
	}

	if err := natskit.PublishJSON(bus, ctx, subject, Msg{Value: 42}); err != nil {
		t.Fatalf("publish json: %v", err)
	}

	if m := recv(t, got, 3*time.Second, "message"); m.Value != 42 {
		t.Errorf("expected value=42, got=%d", m.Value)
	}
}

func TestBus_SubscribeRejectsCancelledContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bus.Subscribe(ctx, "x", func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("want error for cancelled ctx")
	}
}
