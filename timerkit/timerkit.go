// Package timerkit — periodic jobs через fx group: сервис регистрирует
// Job (Name, Interval, Run) в группу `timer.job`, а Module
// подхватывает их в fx.Lifecycle, запускает каждый по тикеру и ловит
// panic'и без падения процесса.
package timerkit

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"go.uber.org/fx"
)

type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
}

type jobsParams struct {
	fx.In
	Jobs []Job `group:"timer.job"`
}

func startJobs(lc fx.Lifecycle, p jobsParams, log *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			for _, job := range p.Jobs {
				j := job
				go runJob(ctx, j, log)
			}
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}

func runJob(ctx context.Context, j Job, log *slog.Logger) {
	ticker := time.NewTicker(j.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.ErrorContext(ctx, "timerkit: panic in job",
							"job", j.Name,
							"panic", r,
							"stack", string(debug.Stack()),
						)
					}
				}()
				if err := j.Run(ctx); err != nil {
					log.ErrorContext(ctx, "timerkit: job error",
						"job", j.Name,
						"error", err,
					)
				}
			}()
		}
	}
}
