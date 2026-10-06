package sync

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// EngineGroup — fx-группа движков синка; Module разворачивает их в timerkit.Job'ы.
const EngineGroup = "hubkit.sync.engine"

// Module провайдит HTTPFetcher (поверх hubkit/client), разворачивает движки
// группы EngineGroup в job'ы timerkit.Group (graceful shutdown — за timerkit)
// и снимает их метрики на остановке приложения.
// Config не provide'ится: caller добавляет `fx.Provide(sync.NewConfig)`.
var Module = fx.Module(
	"hubkit/sync",
	fx.Provide(
		fx.Annotate(NewHTTPFetcher, fx.As(new(Fetcher))),
	),
	timerkit.Provide(jobsFromEngines, fx.ParamTags(`group:"`+EngineGroup+`"`)),
	fx.Invoke(fx.Annotate(closeEnginesOnStop, fx.ParamTags(``, `group:"`+EngineGroup+`"`))),
)

// jobsFromEngines разворачивает движки в timer-jobs (flatten group → group).
func jobsFromEngines(engines []*Engine) []timerkit.Job {
	jobs := make([]timerkit.Job, 0, len(engines))
	for _, e := range engines {
		jobs = append(jobs, e.Job())
	}
	return jobs
}

// closeEnginesOnStop снимает observable-callback'и движков при остановке:
// иначе meter продолжал бы наблюдать серии ресурса, который больше не тикает.
func closeEnginesOnStop(lc fx.Lifecycle, engines []*Engine) {
	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		errs := make([]error, 0, len(engines))
		for _, e := range engines {
			errs = append(errs, e.Close())
		}
		return errors.Join(errs...)
	}})
}

// NewConfig читает HUB_SYNC_* из ENV и валидирует их (ErrConfig на старте).
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
