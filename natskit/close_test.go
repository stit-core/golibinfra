package natskit_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestConn_CloseWaitsForInflightHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn, err := natskit.NewConn(clientConfig(url), logkit.Nop())
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	started := make(chan struct{}, 1)
	var finished atomic.Bool
	if _, err := bus.Subscribe(context.Background(), "slow.work", func(context.Context, []byte) error {
		started <- struct{}{}
		time.Sleep(time.Second)
		finished.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), "slow.work", "x"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 5*time.Second, "handler start")

	begin := time.Now()
	conn.Close()
	if !finished.Load() {
		t.Fatal("Close returned before the in-flight handler finished")
	}
	if elapsed := time.Since(begin); elapsed > 4*time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	if conn.Status() != natsgo.CLOSED {
		t.Fatalf("status after Close = %s, want CLOSED", conn.Status())
	}
	if _, err := bus.Subscribe(context.Background(), "late", func(context.Context, []byte) error { return nil }); !errors.Is(err, natskit.ErrClosed) {
		t.Fatalf("Subscribe after Close err = %v, want ErrClosed", err)
	}
}

func TestConn_CloseGivesUpAfterDrainTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	log := &memLogger{}
	cfg := clientConfig(url)
	cfg.DrainTimeout = 300 * time.Millisecond
	conn, err := natskit.NewConn(cfg, log)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	bus := natskit.NewBus(conn, log, natskit.Config{})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if _, err := bus.Subscribe(context.Background(), "stuck.work", func(context.Context, []byte) error {
		started <- struct{}{}
		<-release
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), "stuck.work", "x"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 5*time.Second, "handler start")

	begin := time.Now()
	conn.Close()
	close(release)
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Fatalf("Close took %v, want ≈DrainTimeout", elapsed)
	}
	if conn.Status() != natsgo.CLOSED {
		t.Fatalf("status after Close = %s, want CLOSED", conn.Status())
	}
	if !log.has("warn", "natskit: drain timeout, handlers still in flight") {
		t.Fatalf("drain timeout not logged: %+v", log.entries)
	}
}

// JetStream: Close ждёт обработчик, и его Ack успевает уйти до закрытия
// соединения — новый инстанс с тем же durable сообщение не получает.
func TestClient_CloseWaitsForJetStreamHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	cfg := clientConfig(url)
	cfg.JetStream = jsConfig("ORDERS").JetStream
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.HandlerTimeout = 10 * time.Second

	client, err := natskit.New("t", cfg, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := make(chan struct{}, 1)
	var finished atomic.Bool
	if _, err := client.Stream().SubscribeStream(context.Background(), "ORDERS", "ORDERS.slow", "slow",
		func(context.Context, []byte) error {
			started <- struct{}{}
			time.Sleep(time.Second)
			finished.Store(true)
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := client.Stream().PublishStream(context.Background(), "ORDERS.slow", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 10*time.Second, "handler start")

	client.Close()
	if !finished.Load() {
		t.Fatal("Close returned before the JetStream handler finished")
	}

	again, err := natskit.New("t", cfg, logkit.Nop())
	if err != nil {
		t.Fatalf("New (again): %v", err)
	}
	t.Cleanup(again.Close)
	redelivered := make(chan struct{}, 1)
	if _, err := again.Stream().SubscribeStream(context.Background(), "ORDERS", "ORDERS.slow", "slow",
		func(context.Context, []byte) error {
			redelivered <- struct{}{}
			return nil
		}); err != nil {
		t.Fatalf("subscribe (again): %v", err)
	}
	expectSilence(t, redelivered, 4*time.Second, "redelivery of an acked message")
}

func TestConn_CloseIsIdempotentAndNilSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	var nilConn *natskit.Conn
	nilConn.Close()

	url := startJetStreamNATS(t)
	conn, err := natskit.NewConn(clientConfig(url), logkit.Nop())
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	conn.Close()
	conn.Close()
	if err := conn.Healthy(); err == nil {
		t.Fatal("Healthy after Close = nil, want error")
	}
}
