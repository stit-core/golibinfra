package natskit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-infra/golibinfra/logkit"
	"git.stit.tech/stit-infra/golibinfra/natskit"
	"git.stit.tech/stit-infra/golibinfra/testkit"
)

func TestBus_PanicRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	nc, err := natsgo.Connect(fix.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	conn := natskit.ExportConn(nc)
	log := logkit.Nop()
	bus := natskit.NewBus(conn, log, natskit.Config{})

	ctx := context.Background()
	subject := "test.panic"

	var afterPanic int32
	if err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		if string(payload) == `"panic"` {
			panic("test panic")
		}
		atomic.AddInt32(&afterPanic, 1)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(ctx, subject, "panic"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := bus.Publish(ctx, subject, "ok"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if atomic.LoadInt32(&afterPanic) != 1 {
		t.Errorf("expected 1 message after panic, got %d", atomic.LoadInt32(&afterPanic))
	}
}

func TestBus_Backpressure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartNATS(t)
	nc, err := natsgo.Connect(fix.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	conn := natskit.ExportConn(nc)
	log := logkit.Nop()

	maxInFlight := 10
	cfg := natskit.Config{
		Backpressure: natskit.BackpressureConfig{MaxInFlight: maxInFlight},
	}
	bus := natskit.NewBus(conn, log, cfg)

	ctx := context.Background()
	subject := "test.backpressure"

	total := 200
	var current, peak int32
	// Buffered to total — non-blocking handler send без потери сигналов
	// при гонке нескольких worker'ов с одним receiver'ом.
	done := make(chan struct{}, total)

	if err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
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

	for i := 0; i < total; i++ {
		if err := bus.Publish(ctx, subject, i); err != nil {
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
