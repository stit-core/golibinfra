package sync

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module провайдит HTTPFetcher (поверх hubkit/client) и превращает движки,
// зарегистрированные продуктом в group `hubkit.sync.engine`, в timerkit.Job'ы
// (group `timer.job` — их подхватит timerkit.Module с graceful shutdown).
//
// Config не provide'ится (asymmetry-нота как в pgkit — caller добавляет
// `fx.Provide(sync.NewConfig)`). Продукт регистрирует по движку на ресурс,
// реализуя ProjectionStore/CursorStore и собирая NewEngine; requires
// timerkit.Module и hubkit/client в графе.
var Module = fx.Module(
	"hubkit-sync",
	fx.Provide(
		fx.Annotate(NewHTTPFetcher, fx.As(new(Fetcher))),
		fx.Annotate(
			jobsFromEngines,
			fx.ParamTags(`group:"hubkit.sync.engine"`),
			fx.ResultTags(`group:"timer.job,flatten"`),
		),
	),
)

// jobsFromEngines разворачивает движки в timer-jobs (flatten group → group).
func jobsFromEngines(engines []*Engine) []timerkit.Job {
	jobs := make([]timerkit.Job, 0, len(engines))
	for _, e := range engines {
		jobs = append(jobs, e.Job())
	}
	return jobs
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
