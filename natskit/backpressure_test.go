package natskit_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestBus_PanicRecovery(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "panic")

	afterPanic := make(chan struct{}, 4)
	if _, err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		if string(payload) == "panic" {
			panic("test panic")
		}
		afterPanic <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(ctx, subject, []byte("panic")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte("ok")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	recv(t, afterPanic, 5*time.Second, "message after panic")
	expectSilence(t, afterPanic, 200*time.Millisecond, "extra delivery")
}

func TestBus_Backpressure(t *testing.T) {
	conn := connect(t, natsURL(t))

	maxInFlight := 10
	cfg := natskit.Config{
		Backpressure: natskit.BackpressureConfig{MaxInFlight: maxInFlight},
	}
	bus := natskit.NewBus(conn, logkit.Nop(), cfg)

	ctx := context.Background()
	subject := subj(t, "backpressure")

	total := 200
	var current, peak int32
	// Buffered to total — non-blocking handler send без потери сигналов
	// при гонке нескольких worker'ов с одним receiver'ом.
	done := make(chan struct{}, total)

	if _, err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		cur := atomic.AddInt32(&current, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for i := range total {
		if err := bus.Publish(ctx, subject, fmt.Appendf(nil, "%d", i)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	received := 0
	timeout := time.After(30 * time.Second)
	for received < total {
		select {
		case <-done:
			received++
		case <-timeout:
			t.Fatalf("timeout: received %d/%d, peak concurrent: %d", received, total, atomic.LoadInt32(&peak))
		}
	}

	p := atomic.LoadInt32(&peak)
	if p > int32(maxInFlight) {
		t.Errorf("peak concurrent %d exceeds maxInFlight %d", p, maxInFlight)
	}
}

// Core-обработчик получает deadline NATS_HANDLER_TIMEOUT: зависший не держит
// слот семафора вечно.
func TestBus_HandlerTimeout(t *testing.T) {
	conn := connect(t, natsURL(t))
	cfg := natskit.Config{HandlerTimeout: 200 * time.Millisecond}
	bus := natskit.NewBus(conn, logkit.Nop(), cfg)

	ctx := context.Background()
	subject := subj(t, "slow")

	cancelled := make(chan error, 1)
	if _, err := bus.Subscribe(ctx, subject, func(hctx context.Context, _ []byte) error {
		<-hctx.Done()
		cancelled <- hctx.Err()
		return hctx.Err()
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := recv(t, cancelled, 5*time.Second, "handler cancellation"); err == nil {
		t.Fatal("handler ctx err = nil, want DeadlineExceeded")
	}
}
