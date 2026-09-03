package timerkit

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

var Module = fx.Module(
	"timerkit",
	fx.Invoke(startJobs),
)

type params struct {
	fx.In
	Jobs []Job `group:"timer.job"`
}

func startJobs(lc fx.Lifecycle, p params, log logkit.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r := newRunner(log)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := validate(p.Jobs); err != nil {
				cancel()
				return err
			}
			for _, j := range p.Jobs {
				wg.Go(func() { r.run(ctx, j) })
			}
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return fmt.Errorf("timerkit: stop: jobs still running: %w", stopCtx.Err())
			}
		},
	})
}
