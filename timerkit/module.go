package timerkit

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/stopbudget"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// JobsGroup — имя fx-группы, из которой Module забирает job'ы.
const JobsGroup = "timerkit.jobs"

// scheduler — маркер Module в графе: ProvideJob требует его, поэтому job без
// запускающего модуля — ошибка сборки графа (missing type timerkit.scheduler).
// Неэкспортируемый: подложить его в граф в обход Module нельзя.
type scheduler struct{}

// Module запускает все job'ы группы JobsGroup в fx-lifecycle приложения.
var Module = fx.Module(
	"timerkit",
	fx.Supply(scheduler{}),
	fx.Invoke(startJobs),
)

// ProvideJob регистрирует конструктор Job (или []Job — группа собирается через
// flatten) в группу JobsGroup и требует Module в графе. Конструктор неверного типа
// и граф без Module отвергаются при сборке: иначе job молча не запускается.
// Дополнительные аннотации (например fx.ParamTags) применяются до групповой;
// свой fx.ResultTags передавать нельзя.
func ProvideJob(ctor any, anns ...fx.Annotation) fx.Option {
	tag, err := groupTag(ctor)
	if err != nil {
		return fx.Error(err)
	}
	return fx.Options(
		fx.Provide(fx.Annotate(ctor, append(anns, fx.ResultTags(tag))...)),
		fx.Invoke(requireModule),
	)
}

func requireModule(scheduler) {}

var (
	jobType  = reflect.TypeFor[Job]()
	jobsType = reflect.TypeFor[[]Job]()
)

func groupTag(ctor any) (string, error) {
	t := reflect.TypeOf(ctor)
	if t == nil || t.Kind() != reflect.Func {
		return "", fmt.Errorf("timerkit: ProvideJob expects a constructor function returning timerkit.Job or []timerkit.Job, got %T", ctor)
	}
	if t.NumOut() > 0 {
		switch t.Out(0) {
		case jobType:
			return `group:"` + JobsGroup + `"`, nil
		case jobsType:
			return `group:"` + JobsGroup + `,flatten"`, nil
		}
	}
	return "", fmt.Errorf("timerkit: ProvideJob constructor must return timerkit.Job or []timerkit.Job as its first result, got %v", t)
}

type params struct {
	fx.In
	Jobs  []Job          `group:"timerkit.jobs"`
	Clock clockkit.Clock `optional:"true"`
}

func startJobs(lc fx.Lifecycle, p params, log logkit.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	// running — имена job'ов, чьи горутины ещё не вернулись.
	var running sync.Map
	r := newRunner(log, p.Clock)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := validate(p.Jobs); err != nil {
				cancel()
				return err
			}
			for _, j := range p.Jobs {
				running.Store(j.Name, struct{}{})
				wg.Go(func() {
					defer running.Delete(j.Name)
					r.run(ctx, j)
				})
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
			// Треть остатка бюджета остановки — хукам после job'ов (шина, пул, telemetry): job,
			// не свернувшийся по отмене ctx, остановку остальных не отнимает.
			wait, stop := stopbudget.Share(stopCtx)
			defer stop()
			select {
			case <-done:
			case <-wait.Done():
				var names []string
				running.Range(func(name, _ any) bool {
					names = append(names, fmt.Sprint(name))
					return true
				})
				slices.Sort(names)
				log.Warn(stopCtx, "timerkit: jobs are still running after the stop budget", "jobs", names)
			}
			return nil
		},
	})
}
