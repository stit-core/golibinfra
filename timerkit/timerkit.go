// Package timerkit — periodic jobs через fx-группу JobsGroup: Module поднимает
// горутину на каждую Job, запускает Run по интервалу (с опциональными дедлайном,
// джиттером и стартовым прогоном), ловит panic'и, эмитит спан и метрики и на
// остановке дожидается завершения запусков.
package timerkit

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/internal/spanerr"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrJob — невалидная Job; возвращается из OnStart и валит старт приложения.
var ErrJob = errors.New("timerkit: invalid job")

// Job — периодическая задача: Run вызывается каждые Interval, пока жив
// fx-lifecycle приложения.
type Job struct {
	// Name — идентификатор в логах, спанах и label'е `job` метрик; обязателен и
	// уникален в пределах приложения.
	Name string
	// Interval — пауза между запусками, отсчитывается от конца предыдущего; > 0.
	Interval time.Duration
	// Run — тело задачи; обязательно. Ошибка логируется и не прерывает цикл.
	Run func(ctx context.Context) error

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

type runner struct {
	log         logkit.Logger
	clock       clockkit.Clock
	tracer      trace.Tracer
	runs        metric.Int64Counter
	runDuration metric.Float64Histogram
}

// newRunner берёт инструменты у текущих глобальных провайдеров: пакетный
// инструмент делегат OTel привязал бы к первому провайдеру процесса.
func newRunner(log logkit.Logger, clock clockkit.Clock) *runner {
	if clock == nil {
		clock = clockkit.Real{}
	}
	m := otelkit.Meter("timerkit")
	return &runner{
		log:    log,
		clock:  clock,
		tracer: otelkit.Tracer("timerkit"),
		runs: otelkit.Must(m.Int64Counter("timer_job_runs_total",
			metric.WithDescription("Timer job runs by result"),
		)),
		runDuration: otelkit.Must(m.Float64Histogram("timer_job_duration_seconds",
			metric.WithDescription("Timer job run duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
	}
}

// run: ожидание отсчитывается от завершения предыдущего запуска, запуски одной
// Job не перекрываются.
func (r *runner) run(ctx context.Context, j Job) {
	if j.RunOnStart {
		r.once(ctx, j)
	}
	for {
		timer := r.clock.NewTimer(wait(j))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
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
	ctx, span := r.tracer.Start(ctx, "timerkit.job "+j.Name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("timer.job", j.Name)),
	)
	defer span.End()

	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if j.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, j.Timeout)
	}
	defer cancel()

	start := r.clock.Now()
	result := "ok"
	func() {
		defer func() {
			if p := recover(); p != nil {
				result = "panic"
				spanerr.Record(span, fmt.Errorf("panic: %v", p), r.log)
				span.SetStatus(codes.Error, "panic")
				r.log.Error(ctx, "timerkit: panic in job", "job", j.Name, "panic", p, "stack", string(debug.Stack()))
			}
		}()
		if err := j.Run(runCtx); err != nil {
			result = "error"
			// Текст ошибки job'а — потребителя: в спан он идёт под теми же масками, что в лог.
			spanerr.Record(span, err, r.log)
			r.log.Error(ctx, "timerkit: job error", "job", j.Name, "error", err)
		}
	}()

	r.runDuration.Record(ctx, r.clock.Since(start).Seconds(), metric.WithAttributes(attribute.String("job", j.Name)))
	r.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("job", j.Name), attribute.String("result", result)))
}
