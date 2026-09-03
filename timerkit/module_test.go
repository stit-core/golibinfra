package timerkit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func newApp(t *testing.T, jobs ...Job) *fx.App {
	t.Helper()
	opts := []fx.Option{
		fx.NopLogger,
		fx.Provide(func() logkit.Logger { return logkit.Nop() }),
		Module,
	}
	for _, j := range jobs {
		opts = append(opts, fx.Supply(fx.Annotate(j, fx.ResultTags(`group:"timer.job"`))))
	}
	app := fx.New(opts...)
	if err := app.Err(); err != nil {
		t.Fatalf("fx.New: %v", err)
	}
	return app
}

func TestModule_StartRunsJobAndStopWaits(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	app := newApp(t, Job{
		Name: "greeter", Interval: time.Hour, RunOnStart: true,
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil
		},
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
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
	app := newApp(t, Job{Name: "broken", Interval: 0, Run: func(context.Context) error { return nil }})
	err := app.Start(context.Background())
	if !errors.Is(err, ErrJob) || !strings.Contains(err.Error(), `"broken"`) {
		t.Fatalf("Start err = %v, want ErrJob naming the job", err)
	}
}

func TestModule_StopHonoursDeadline(t *testing.T) {
	release := make(chan struct{})
	app := newApp(t, Job{
		Name: "stuck", Interval: time.Hour, RunOnStart: true,
		Run: func(context.Context) error {
			<-release
			return nil
		},
	})
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
