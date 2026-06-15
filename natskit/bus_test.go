package natskit_test

import (
	"context"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

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
		if err := bus.SubscribeQueue(ctx, "work.items", "workers", h); err != nil {
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
	select {
	case <-got:
		t.Fatal("дубликат доставки — queue-group не балансирует")
	case <-time.After(500 * time.Millisecond):
	}
}

func connect(t *testing.T, url string) *natskit.Conn {
	t.Helper()
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	conn := natskit.ExportConn(nc)
	return conn
}

func TestBus_PublishSubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	log := logkit.Nop()
	bus := natskit.NewBus(conn, log, natskit.Config{})

	ctx := context.Background()
	subject := "test.roundtrip"

	got := make(chan []byte, 1)
	if err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		got <- payload
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(ctx, subject, map[string]string{"hello": "world"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case payload := <-got:
		if string(payload) != `{"hello":"world"}` {
			t.Errorf("unexpected payload: %s", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestBus_PublishSubscribeJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	log := logkit.Nop()
	bus := natskit.NewBus(conn, log, natskit.Config{})

	ctx := context.Background()
	subject := "test.json"

	type Msg struct {
		Value int `json:"value"`
	}

	got := make(chan Msg, 1)
	if err := natskit.SubscribeJSON(bus, ctx, subject, func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe json: %v", err)
	}

	if err := natskit.PublishJSON(bus, ctx, subject, Msg{Value: 42}); err != nil {
		t.Fatalf("publish json: %v", err)
	}

	select {
	case m := <-got:
		if m.Value != 42 {
			t.Errorf("expected value=42, got=%d", m.Value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}
