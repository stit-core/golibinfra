package natskit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestBus_SubscriptionUnsubscribe(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
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
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
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
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
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

// Сообщение, полученное до Unsubscribe, но не начатое — его callback ждёт слот
// MaxInFlight, занятый соседней подпиской, — обработчик не получает: Unsubscribe
// снимает интерес сразу, и слот, освободившийся позже, его не запускает.
func TestSubscription_UnsubscribeSkipsMessageWaitingForSlot(t *testing.T) {
	ctx := context.Background()
	type subscribeFunc func(subject, durable string, h natskit.Handler) (natskit.Subscription, error)
	run := func(t *testing.T, subscribe subscribeFunc, publish func(subject string) error, delivered func(durable string) bool) {
		t.Helper()
		busy, release := make(chan struct{}, 1), make(chan struct{})
		if _, err := subscribe(subj(t, "busy"), "busy", func(context.Context, []byte) error {
			busy <- struct{}{}
			<-release
			return nil
		}); err != nil {
			t.Fatalf("subscribe busy: %v", err)
		}
		got := make(chan struct{}, 1)
		sub, err := subscribe(subj(t, "waiting"), "waiting", func(context.Context, []byte) error {
			got <- struct{}{}
			return nil
		})
		if err != nil {
			t.Fatalf("subscribe waiting: %v", err)
		}
		if err := publish(subj(t, "busy")); err != nil {
			t.Fatalf("publish busy: %v", err)
		}
		recv(t, busy, 10*time.Second, "busy handler start")
		if err := publish(subj(t, "waiting")); err != nil {
			t.Fatalf("publish waiting: %v", err)
		}
		eventually(t, 10*time.Second, "waiting message delivered to the client", func() bool { return delivered("waiting") })
		time.Sleep(200 * time.Millisecond) // callback доставки встаёт на семафор
		if err := sub.Unsubscribe(); err != nil {
			t.Fatalf("unsubscribe: %v", err)
		}
		close(release)
		expectSilence(t, got, time.Second, "message handled after Unsubscribe")
	}

	t.Run("core", func(t *testing.T) {
		bus := newBus(t, connect(t, natsURL(t)), logkit.Nop(), natskit.Config{Backpressure: natskit.BackpressureConfig{MaxInFlight: 1}})
		var published atomic.Bool
		run(t,
			func(subject, _ string, h natskit.Handler) (natskit.Subscription, error) {
				return bus.Subscribe(ctx, subject, h)
			},
			func(subject string) error { published.Store(true); return bus.Publish(ctx, subject, []byte(`{}`)) },
			func(string) bool { return published.Load() })
	})

	t.Run("jetstream", func(t *testing.T) {
		stream := nameFor(t)
		cfg := jsConfig(stream)
		cfg.JetStream.MaxInFlight = 1 // один слот на обе подписки инстанса
		sb := newStreamBus(t, cfg)
		nc, err := natsgo.Connect(natsURL(t))
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatalf("jetstream: %v", err)
		}
		run(t,
			func(subject, durable string, h natskit.Handler) (natskit.Subscription, error) {
				return sb.SubscribeStream(ctx, stream, subject, durable, h)
			},
			func(subject string) error { return sb.PublishStream(ctx, subject, []byte(`{}`)) },
			func(durable string) bool {
				c, err := js.Consumer(ctx, stream, durable)
				if err != nil {
					return false
				}
				info, err := c.Info(ctx)
				return err == nil && info.NumAckPending == 1
			})
	})
}
