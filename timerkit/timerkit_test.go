package timerkit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunJob_CallsAtLeastTwice(t *testing.T) {
	var count atomic.Int32
	job := Job{
		Name:     "test-job",
		Interval: 20 * time.Millisecond,
		Run: func(ctx context.Context) error {
			count.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go runJob(ctx, job, slog.Default())

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if count.Load() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected Run to be called >=2 times, got %d", count.Load())
}

func TestRunJob_StopsOnContextCancel(t *testing.T) {
	var count atomic.Int32
	job := Job{
		Name:     "cancel-test",
		Interval: 10 * time.Millisecond,
		Run: func(ctx context.Context) error {
			count.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runJob(ctx, job, slog.Default())
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("goroutine did not stop after context cancel")
	}
}

func TestRunJob_PanicRecovery(t *testing.T) {
	var count atomic.Int32
	job := Job{
		Name:     "panic-job",
		Interval: 20 * time.Millisecond,
		Run: func(ctx context.Context) error {
			n := count.Add(1)
			if n == 1 {
				panic("test panic")
			}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go runJob(ctx, job, slog.Default())

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if count.Load() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected job to continue after panic, got %d calls", count.Load())
}
