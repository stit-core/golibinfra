package timerkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func newApp(t *testing.T, opts ...fx.Option) *fx.App {
	t.Helper()
	app := fx.New(append([]fx.Option{
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		Module,
	}, opts...)...)
	if err := app.Err(); err != nil {
		t.Fatalf("fx.New: %v", err)
	}
	return app
}

func supplyJob(j Job) fx.Option {
	return ProvideJob(func() Job { return j })
}

// waitFor ждёт сигнал с дедлайном: job, потерянный графом, валит тест причиной, а не вешает
// пакет до таймаута go test.
func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no signal after 5s", what)
	}
}

// waitTimers — clock.WaitTimers с дедлайном.
func waitTimers(t *testing.T, clock *clockkit.Tickable, n int) {
	t.Helper()
	done := make(chan struct{})
	go func() { clock.WaitTimers(n); close(done) }()
	waitFor(t, done, "job timers")
}

func TestModule_StartRunsJobAndStopWaits(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	app := newApp(t, supplyJob(Job{
		Name: "greeter", Interval: time.Hour, RunOnStart: true,
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil
		},
	}))
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, started, "job start")
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Stop returned before the running job finished")
	}
}

func TestModule_InvalidJobFailsOnStart(t *testing.T) {
	app := newApp(t, supplyJob(Job{Name: "broken", Interval: 0, Run: func(context.Context) error { return nil }}))
	err := app.Start(context.Background())
	if !errors.Is(err, ErrJob) || !strings.Contains(err.Error(), `"broken"`) {
		t.Fatalf("Start err = %v, want ErrJob naming the job", err)
	}
}

func TestModule_StopHonoursDeadline(t *testing.T) {
	release := make(chan struct{})
	app := newApp(t, supplyJob(Job{
		Name: "stuck", Interval: time.Hour, RunOnStart: true,
		Run: func(context.Context) error {
			<-release
			return nil
		},
	}))
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := app.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop err = %v, want DeadlineExceeded while the job ignores ctx", err)
	}
}

// Расписание идёт по clockkit.Clock из графа: тест двигает часы и не спит.
func TestModule_SchedulesOnClockFromGraph(t *testing.T) {
	clock := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	runs := make(chan struct{}, 1)
	app := newApp(t,
		fx.Supply(fx.Annotate(clock, fx.As(new(clockkit.Clock)))),
		supplyJob(Job{
			Name: "scheduled", Interval: time.Hour,
			Run: func(context.Context) error {
				runs <- struct{}{}
				return nil
			},
		}),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	waitTimers(t, clock, 1)
	select {
	case <-runs:
		t.Fatal("job ran before the interval elapsed")
	default:
	}
	clock.Advance(time.Hour)
	waitFor(t, runs, "job run after the interval")
}

// Регрессия: конструктор неверного типа кладёт значение в группу другим типом,
// и job молча не запускается.
func TestProvideJob_RejectsWrongConstructor(t *testing.T) {
	tests := []struct {
		name string
		ctor any
	}{
		{name: "returns bare func", ctor: func() func(context.Context) error {
			return func(context.Context) error { return nil }
		}},
		{name: "returns error only", ctor: func() error { return nil }},
		{name: "returns nothing", ctor: func() {}},
		{name: "job second", ctor: func() (error, Job) { return nil, Job{} }}, //nolint:staticcheck // проверяется именно неверный порядок результатов
		{name: "not a function", ctor: Job{}},
		{name: "nil", ctor: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fx.New(fx.NopLogger, ProvideJob(tt.ctor))
			err := app.Err()
			if err == nil {
				t.Fatal("want error from fx.New, got nil")
			}
			if !strings.Contains(err.Error(), "timerkit: ProvideJob") {
				t.Fatalf("error must name timerkit.ProvideJob: %v", err)
			}
		})
	}
}

// ProvideJob кладёт в ту же группу, из которой читает Module: и одиночную Job, и
// срез (flatten), и конструктор с ошибкой.
func TestProvideJob_LandsInGroupModuleReads(t *testing.T) {
	ran := make(chan string, 3)
	job := func(name string) Job {
		return Job{Name: name, Interval: time.Hour, RunOnStart: true, Run: func(context.Context) error {
			ran <- name
			return nil
		}}
	}
	app := newApp(t,
		ProvideJob(func() Job { return job("single") }),
		ProvideJob(func() (Job, error) { return job("with-error"), nil }),
		ProvideJob(func() []Job { return []Job{job("flat-1"), job("flat-2")} }),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	seen := map[string]bool{}
	for range 4 {
		select {
		case name := <-ran:
			seen[name] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("jobs that ran: %v, want all four", seen)
		}
	}
}

// Аннотации вызывающего (параметр из другой группы) сочетаются с групповой.
func TestProvideJob_KeepsCallerAnnotations(t *testing.T) {
	type source struct{ name string }
	app := newApp(t,
		fx.Provide(fx.Annotate(func() source { return source{name: "engine"} }, fx.ResultTags(`group:"sources"`))),
		ProvideJob(
			func(sources []source) []Job {
				jobs := make([]Job, 0, len(sources))
				for _, s := range sources {
					jobs = append(jobs, Job{Name: s.name, Interval: time.Hour, Run: func(context.Context) error { return nil }})
				}
				return jobs
			},
			fx.ParamTags(`group:"sources"`),
		),
		fx.Invoke(fx.Annotate(func(jobs []Job) {
			if len(jobs) != 1 || jobs[0].Name != "engine" {
				t.Errorf("jobs in group = %v, want one named engine", jobs)
			}
		}, fx.ParamTags(`group:"`+JobsGroup+`"`))),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
}

// Регрессия FX-04: job без Module в графе никто не запускает — это должна быть
// ошибка сборки графа, а не молча стоящий outbox или неочищаемая таблица.
func TestProvideJob_RequiresModule(t *testing.T) {
	job := ProvideJob(func() Job {
		return Job{Name: "orphan", Interval: time.Hour, Run: func(context.Context) error { return nil }}
	})
	err := fx.ValidateApp(fx.NopLogger, fx.Provide(logkit.Nop), job)
	if err == nil || !strings.Contains(err.Error(), "timerkit.scheduler") {
		t.Fatalf("ValidateApp without timerkit.Module: err = %v, want missing timerkit.scheduler", err)
	}
	if err := fx.ValidateApp(fx.NopLogger, fx.Provide(logkit.Nop), Module, job); err != nil {
		t.Fatalf("ValidateApp with timerkit.Module: %v", err)
	}
	// Повторный Module — ошибка сборки, а не двойной запуск job'ов.
	err = fx.ValidateApp(fx.NopLogger, fx.Provide(logkit.Nop), Module, Module, job)
	if err == nil || !strings.Contains(err.Error(), "already provided") {
		t.Fatalf("ValidateApp with timerkit.Module twice: err = %v, want already provided", err)
	}
}
