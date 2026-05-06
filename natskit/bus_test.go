package natskit_test

import (
	"context"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/akadmin/golibinfra/logkit"
	"git.stit.tech/akadmin/golibinfra/natskit"
	"git.stit.tech/akadmin/golibinfra/testkit"
)

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
	bus := natskit.NewBus(conn, log)

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
	bus := natskit.NewBus(conn, log)

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
