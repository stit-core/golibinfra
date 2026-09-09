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
	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()
	subject := subj(t, "one-off")

	got := make(chan struct{}, 4)
	sub, err := bus.Subscribe(ctx, subject, func(context.Context, []byte) error {
		got <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, got, 3*time.Second, "first message")

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, got, 500*time.Millisecond, "delivery after Unsubscribe")
}

func TestBus_SubscriptionDrainWaitsForHandler(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()
	subject := subj(t, "drain")

	started := make(chan struct{}, 2)
	var finished atomic.Bool
	sub, err := bus.Subscribe(ctx, subject, func(context.Context, []byte) error {
		started <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
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

	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, started, time.Second, "delivery after Drain")
}

func TestBus_SubscriptionDrainHonoursContext(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()
	subject := subj(t, "stuck")

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	sub, err := bus.Subscribe(ctx, subject, func(context.Context, []byte) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
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
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	ctx := context.Background()
	subject := subj(t, "drain")

	started := make(chan struct{}, 2)
	var finished atomic.Bool
	sub, err := sb.SubscribeStream(ctx, stream, subject, "drain", func(context.Context, []byte) error {
		started <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		finished.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(ctx, subject, []byte(`{}`)); err != nil {
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

	if err := sb.PublishStream(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, started, time.Second, "delivery after Drain")
}

// Unsubscribe останавливает pull-консьюмер: доставки прекращаются сразу,
// неподтверждённое сообщение сервер переотправит после AckWait.
func TestStreamBus_SubscriptionUnsubscribe(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	ctx := context.Background()
	subject := subj(t, "unsub")

	got := make(chan struct{}, 4)
	sub, err := sb.SubscribeStream(ctx, stream, subject, "unsub", func(context.Context, []byte) error {
		got <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, got, 10*time.Second, "first message")

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := sb.PublishStream(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, got, time.Second, "delivery after Unsubscribe")
}
