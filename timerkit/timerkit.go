// Package timerkit — periodic jobs через fx group `timer.job`: Module
// поднимает горутину на каждую Job, запускает Run по интервалу (с опциональными
// дедлайном, джиттером и стартовым прогоном), ловит panic'и, эмитит метрики и
// на остановке дожидается завершения запусков.
package timerkit

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

var ErrJob = errors.New("timerkit: invalid job")

type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error

	// Timeout — дедлайн ctx одного запуска; 0 — без дедлайна.
	Timeout time.Duration
	// Jitter — случайная добавка [0, Jitter) к каждому ожиданию; 0 — без джиттера.
	Jitter time.Duration
	// RunOnStart — первый запуск сразу на старте, а не через Interval.
	RunOnStart bool
}

func (j Job) validate() error {
	switch {
	case j.Name == "":
		return fmt.Errorf("%w: Name is empty", ErrJob)
	case j.Run == nil:
		return fmt.Errorf("%w: job %q: Run is nil", ErrJob, j.Name)
	case j.Interval <= 0:
		return fmt.Errorf("%w: job %q: Interval must be > 0, got %v", ErrJob, j.Name, j.Interval)
	case j.Timeout < 0 || j.Jitter < 0:
		return fmt.Errorf("%w: job %q: Timeout and Jitter must be >= 0", ErrJob, j.Name)
	}
	return nil
}

func validate(jobs []Job) error {
	seen := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		if err := j.validate(); err != nil {
			return err
		}
		if _, dup := seen[j.Name]; dup {
			return fmt.Errorf("%w: duplicate job name %q", ErrJob, j.Name)
		}
		seen[j.Name] = struct{}{}
	}
	return nil
}

var (
	jobRuns = otelkit.Must(otelkit.Meter("timerkit").Int64Counter(
		"timer_job_runs_total",
		metric.WithDescription("Timer job runs by result"),
	))
	jobDuration = otelkit.Must(otelkit.Meter("timerkit").Float64Histogram(
		"timer_job_duration_seconds",
		metric.WithDescription("Timer job run duration"),
		metric.WithUnit("s"),
	))
)

type runner struct {
	log logkit.Logger
	// after — time.After в проде; тесты подставляют управляемый таймер.
	after func(time.Duration) <-chan time.Time
}

func newRunner(log logkit.Logger) *runner {
	return &runner{log: log, after: time.After}
}

// run: ожидание отсчитывается от завершения предыдущего запуска, запуски одной
// Job не перекрываются.
func (r *runner) run(ctx context.Context, j Job) {
	if j.RunOnStart {
		r.once(ctx, j)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.after(wait(j)):
			if ctx.Err() != nil {
				return
			}
			r.once(ctx, j)
		}
	}
}

func wait(j Job) time.Duration {
	if j.Jitter > 0 {
		return j.Interval + rand.N(j.Jitter) //nolint:gosec // джиттер расписания, не криптография
	}
	return j.Interval
}

func (r *runner) once(ctx context.Context, j Job) {
	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if j.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, j.Timeout)
	}
	defer cancel()

	start := time.Now()
	result := "ok"
	func() {
		defer func() {
			if p := recover(); p != nil {
				result = "panic"
				r.log.Error(ctx, "timerkit: panic in job", "job", j.Name, "panic", p, "stack", string(debug.Stack()))
			}
		}()
		if err := j.Run(runCtx); err != nil {
			result = "error"
			r.log.Error(ctx, "timerkit: job error", "job", j.Name, "error", err)
		}
	}()

	jobDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("job", j.Name)))
	jobRuns.Add(ctx, 1, metric.WithAttributes(attribute.String("job", j.Name), attribute.String("result", result)))
}
