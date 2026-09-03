package natskit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestBus_SubscriptionUnsubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()

	got := make(chan struct{}, 4)
	sub, err := bus.Subscribe(ctx, "one.off", func(context.Context, []byte) error {
		got <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, "one.off", 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, got, 3*time.Second, "first message")

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := bus.Publish(ctx, "one.off", 2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, got, 500*time.Millisecond, "delivery after Unsubscribe")
}

func TestBus_SubscriptionDrainWaitsForHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()

	started := make(chan struct{}, 1)
	var finished atomic.Bool
	sub, err := bus.Subscribe(ctx, "drain.me", func(context.Context, []byte) error {
		started <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, "drain.me", 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 3*time.Second, "handler start")

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sub.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !finished.Load() {
		t.Fatal("Drain returned before the handler finished")
	}

	if err := bus.Publish(ctx, "drain.me", 2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(started) != 0 {
		t.Fatal("subscription still receives after Drain")
	}
}

func TestBus_SubscriptionDrainHonoursContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	sub, err := bus.Subscribe(ctx, "stuck", func(context.Context, []byte) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, "stuck", 1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 3*time.Second, "handler start")

	drainCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := sub.Drain(drainCtx); err == nil {
		t.Fatal("Drain with a stuck handler returned nil, want ctx error")
	}
	close(release)
}

func TestStreamBus_SubscriptionDrainWaitsForHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	ctx := context.Background()

	started := make(chan struct{}, 1)
	var finished atomic.Bool
	sub, err := sb.SubscribeStream(ctx, "ORDERS", "ORDERS.drain", "drain", func(context.Context, []byte) error {
		started <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(ctx, "ORDERS.drain", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, started, 10*time.Second, "handler start")

	drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sub.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !finished.Load() {
		t.Fatal("Drain returned before the handler finished")
	}

	if err := sb.PublishStream(ctx, "ORDERS.drain", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, started, time.Second, "delivery after Drain")
}
