package timerkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// Часы теста двигает сам тест: срабатывание таймера происходит внутри Advance,
// поэтому проверки «ещё не запустился» / «запустился» не требуют sleep.
type harness struct {
	clock *clockkit.Tickable
	runs  chan context.Context
	stop  context.CancelFunc
	done  chan struct{}
}

func start(t *testing.T, j Job, hook func(ctx context.Context) error) *harness {
	t.Helper()
	h := &harness{
		clock: clockkit.NewTickable(time.Unix(1_700_000_000, 0)),
		runs:  make(chan context.Context, 16),
		done:  make(chan struct{}),
	}
	j.Run = func(ctx context.Context) error {
		h.runs <- ctx
		if hook != nil {
			return hook(ctx)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	r := newRunner(logkit.Nop(), h.clock)
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

// tick дожидается, пока runner поставит таймер, и двигает часы на d.
func (h *harness) tick(d time.Duration) {
	h.clock.WaitTimers(1)
	h.clock.Advance(d)
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

func TestRunner_WaitsFullIntervalBetweenRuns(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: 7 * time.Second}, nil)

	h.assertNoRun(t)
	h.tick(6 * time.Second)
	h.assertNoRun(t)
	h.clock.Advance(time.Second)
	h.waitRun(t)

	h.tick(7 * time.Second)
	h.waitRun(t)
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

	h.tick(10*time.Second - time.Nanosecond)
	h.assertNoRun(t) // ожидание не короче Interval
	h.clock.Advance(2 * time.Second)
	h.waitRun(t) // и не длиннее Interval+Jitter
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
		h.tick(time.Second)
		h.waitRun(t)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3 (loop must survive panic and error)", got)
	}
}

func TestRunner_StopsOnCancelWhileWaiting(t *testing.T) {
	h := start(t, Job{Name: "j", Interval: time.Hour}, nil)
	h.clock.WaitTimers(1)
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
	before := metrictest.Collect(t)

	r := newRunner(logkit.Nop(), clockkit.NewTickable(time.Unix(0, 0)))
	ctx := context.Background()
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { return nil }})
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { return errors.New("x") }})
	r.once(ctx, Job{Name: "m", Run: func(context.Context) error { panic("p") }})

	d := metrictest.Collect(t).Sub(before)
	for _, result := range []string{"ok", "error", "panic"} {
		if s := d.Get(t, "timer_job_runs_total", "job", "m", "result", result); s.Kind != metrictest.Counter || s.Value != 1 {
			t.Errorf("timer_job_runs_total: %v (%s), want counter delta 1", s, s.Kind)
		}
	}
	dur := d.Get(t, "timer_job_duration_seconds", "job", "m")
	if dur.Kind != metrictest.Histogram || dur.Unit != "s" || dur.Count != 3 {
		t.Errorf("timer_job_duration_seconds: %v (%s, unit %q), want histogram in s with 3 runs", dur, dur.Kind, dur.Unit)
	}
	// Общие бакеты длительностей: секундная сетка, а не миллисекундные дефолты OTel.
	if want := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}; !slices.Equal(dur.Bounds, want) {
		t.Errorf("duration buckets = %v, want %v", dur.Bounds, want)
	}
}

// Регрессия: без root-спана на запуск инструментированные вызовы внутри Run
// становятся сиротскими трейсами, а длительность job не связать с трейсом.
func TestSpanPerRun(t *testing.T) {
	r := newRunner(logkit.Nop(), clockkit.NewTickable(time.Unix(0, 0)))
	ctx := context.Background()

	var inner trace.SpanContext
	r.once(ctx, Job{Name: "span-ok", Run: func(ctx context.Context) error {
		_, s := otel.Tracer("test").Start(ctx, "inner")
		inner = s.SpanContext()
		s.End()
		return nil
	}})
	r.once(ctx, Job{Name: "span-err", Run: func(context.Context) error { return errors.New("boom") }})
	r.once(ctx, Job{Name: "span-panic", Run: func(context.Context) error { panic("boom") }})

	ok := spanByName(t, "timerkit.job span-ok")
	if ok.Parent().IsValid() {
		t.Errorf("job span must be a root span, parent = %v", ok.Parent())
	}
	if ok.SpanKind() != trace.SpanKindInternal {
		t.Errorf("span kind = %v, want internal", ok.SpanKind())
	}
	if ok.InstrumentationScope().Name != otelkit.Scope("timerkit") {
		t.Errorf("scope = %q, want %q", ok.InstrumentationScope().Name, otelkit.Scope("timerkit"))
	}
	if !hasAttr(ok, "timer.job", "span-ok") {
		t.Errorf("attributes = %v, want timer.job=span-ok", ok.Attributes())
	}
	if ok.Status().Code != codes.Unset {
		t.Errorf("successful run status = %v, want unset", ok.Status())
	}
	if inner.SpanID() == ok.SpanContext().SpanID() || inner.TraceID() != ok.SpanContext().TraceID() {
		t.Errorf("inner span %v is not a child of the job span %v", inner, ok.SpanContext())
	}

	failed := spanByName(t, "timerkit.job span-err")
	if failed.Status().Code != codes.Error || !strings.Contains(failed.Status().Description, "boom") {
		t.Errorf("failed run status = %v, want error naming the failure", failed.Status())
	}
	if len(failed.Events()) == 0 {
		t.Error("failed run must record the error as a span event")
	}

	panicked := spanByName(t, "timerkit.job span-panic")
	if panicked.Status().Code != codes.Error || panicked.Status().Description != "panic" {
		t.Errorf("panicked run status = %v, want error/panic", panicked.Status())
	}
}

// Регрессия ROBS-03: текст ошибки и panic job'а — потребителя — в спане под теми же масками, что
// в записи лога: e-mail и токен из ошибки не лежат в трассе открытыми.
func TestSpan_JobErrorTextIsMasked(t *testing.T) {
	const email, token = "ivan.petrov@example.com", "sk_live_51Mabcdef0123456789abcdef0123"
	failure := fmt.Errorf("charge customer %s: provider rejected Authorization: Bearer %s", email, token)
	log := logtest.Capture()
	r := newRunner(log, clockkit.NewTickable(time.Unix(0, 0)))
	r.once(context.Background(), Job{Name: "pii-err", Run: func(context.Context) error { return failure }})
	r.once(context.Background(), Job{Name: "pii-panic", Run: func(context.Context) error { panic(failure) }})

	logged, _ := log.Find(slog.LevelError, "timerkit: job error")
	failed := spanByName(t, "timerkit.job pii-err")
	texts := []string{failed.Status().Description}
	for _, name := range []string{"timerkit.job pii-err", "timerkit.job pii-panic"} {
		for _, e := range spanByName(t, name).Events() {
			for _, kv := range e.Attributes {
				if e.Name == "exception" && kv.Key == "exception.message" {
					texts = append(texts, kv.Value.AsString())
				}
			}
		}
	}
	if len(texts) != 3 {
		t.Fatalf("spans carry %d error texts, want the status and two exception events: %q", len(texts), texts)
	}
	for _, text := range texts {
		if strings.Contains(text, email) || strings.Contains(text, token) || !strings.Contains(text, "provider rejected") {
			t.Errorf("span text must carry the error under the log masks: %q", text)
		}
	}
	if texts[0] != logged.Str("error") {
		t.Errorf("span status %q and the log record %q must carry the same masked text", texts[0], logged.Str("error"))
	}
}

// Рекордер один на пакет и копит спаны между прогонами, поэтому берётся
// последний совпадающий: под -count=N тест обязан видеть свой запуск.
func spanByName(t *testing.T, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range slices.Backward(spanRecorder.Ended()) {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("span %q not recorded", name)
	return nil
}

func hasAttr(s sdktrace.ReadOnlySpan, key, want string) bool {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key && kv.Value.AsString() == want {
			return true
		}
	}
	return false
}
