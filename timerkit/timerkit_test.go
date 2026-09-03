package timerkit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// fakeTimer: after() отдаёт общий канал; fire() блокируется, пока runner не
// заберёт тик — так тест точно знает, что запуск начался.
type fakeTimer struct {
	mu    sync.Mutex
	waits []time.Duration
	ch    chan time.Time
}

func newFakeTimer() *fakeTimer { return &fakeTimer{ch: make(chan time.Time)} }

func (f *fakeTimer) after(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	return f.ch
}

func (f *fakeTimer) fire() { f.ch <- time.Now() }

func (f *fakeTimer) requested() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

type harness struct {
	timer *fakeTimer
	runs  chan context.Context
	stop  context.CancelFunc
	done  chan struct{}
}

func start(t *testing.T, j Job, hook func(ctx context.Context) error) *harness {
	t.Helper()
	h := &harness{timer: newFakeTimer(), runs: make(chan context.Context, 16), done: make(chan struct{})}
	j.Run = func(ctx context.Context) error {
		h.runs <- ctx
		if hook != nil {
			return hook(ctx)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	r := newRunner(logkit.Nop())
	r.after = h.timer.after
	go func() {
		r.run(ctx, j)
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-h.done
	})
	return h
}

func (h *harness) waitRun(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-h.runs:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("Run was not called")
		return nil
	}
}

func (h *harness) assertNoRun(t *testing.T) {
	t.Helper()
	select {
	case <-h.runs:
		t.Fatal("unexpected Run call")
	default:
	}
}

func TestRunner_TickCallsRunAndWaitsInterval(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: 7 * time.Second}, nil)

	h.assertNoRun(t)
	h.timer.fire()
	h.waitRun(t)
	h.timer.fire()
	h.waitRun(t)

	if w := h.timer.requested(); len(w) < 2 || w[0] != 7*time.Second || w[1] != 7*time.Second {
		t.Fatalf("requested waits = %v, want 7s each", w)
	}
}

func TestRunner_RunOnStart(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: time.Minute, RunOnStart: true}, nil)
	h.waitRun(t)
}

func TestRunner_TimeoutSetsDeadline(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: time.Minute, Timeout: 3 * time.Second, RunOnStart: true}, nil)
	ctx := h.waitRun(t)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		t.Fatalf("deadline = %v, %v; want ~3s from now", deadline, ok)
	}

	h2 := start(t, Job{Name: "j2", Interval: time.Minute, RunOnStart: true}, nil)
	if _, ok := h2.waitRun(t).Deadline(); ok {
		t.Fatal("Timeout=0 must not set a deadline")
	}
}

func TestRunner_JitterWithinRange(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: 10 * time.Second, Jitter: 2 * time.Second}, nil)
	h.timer.fire()
	h.waitRun(t)
	for _, w := range h.timer.requested() {
		if w < 10*time.Second || w >= 12*time.Second {
			t.Fatalf("wait %v outside [10s, 12s)", w)
		}
	}
}

func TestRunner_SurvivesErrorAndPanic(t *testing.T) {
	var calls atomic.Int32
	h := start(t, Job{Name: "j", Interval: time.Second}, func(context.Context) error {
		switch calls.Add(1) {
		case 1:
			panic("boom")
		case 2:
			return errors.New("fail")
		}
		return nil
	})
	for range 3 {
		h.timer.fire()
		h.waitRun(t)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3 (loop must survive panic and error)", got)
	}
}

func TestRunner_StopsOnCancelWhileWaiting(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: time.Hour}, nil)
	h.stop()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not exit after cancel")
	}
}

func TestRunner_CancelledContextIsPassedToRun(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: time.Hour, RunOnStart: true}, nil)
	ctx := h.waitRun(t)
	h.stop()
	<-ctx.Done()
}

func TestValidate(t *testing.T) {
	run := func(context.Context) error { return nil }
	bad := map[string][]Job{
		"empty name":       {{Interval: time.Second, Run: run}},
		"nil run":          {{Name: "a", Interval: time.Second}},
		"zero interval":    {{Name: "a", Run: run}},
		"negative timeout": {{Name: "a", Interval: time.Second, Run: run, Timeout: -1}},
		"negative jitter":  {{Name: "a", Interval: time.Second, Run: run, Jitter: -1}},
		"duplicate":        {{Name: "a", Interval: time.Second, Run: run}, {Name: "a", Interval: time.Second, Run: run}},
	}
	for name, jobs := range bad {
		if err := validate(jobs); !errors.Is(err, ErrJob) {
			t.Errorf("%s: err = %v, want ErrJob", name, err)
		}
	}
	if err := validate([]Job{{Name: "a", Interval: time.Second, Run: run}, {Name: "b", Interval: time.Second, Run: run}}); err != nil {
		t.Errorf("valid jobs: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	before := snapshot(t)

	r := newRunner(logkit.Nop())
	ctx := context.Background()
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { return nil }})
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { return errors.New("x") }})
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { panic("p") }})

	after := snapshot(t)
	for _, k := range []string{"m/ok", "m/error", "m/panic"} {
		if got := after.runs[k] - before.runs[k]; got != 1 {
			t.Errorf("timer_job_runs_total{%s} delta = %d, want 1", k, got)
		}
	}
	if got := after.durations["m"] - before.durations["m"]; got != 3 {
		t.Errorf("timer_job_duration_seconds{job=m} count delta = %d, want 3", got)
	}
}
