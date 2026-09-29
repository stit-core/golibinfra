package timerkit

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Group — имя fx-группы, из которой Module забирает job'ы.
const Group = "timerkit.jobs"

// Scheduler — маркер Module в графе: Provide требует его, поэтому job без
// запускающего модуля — ошибка сборки графа (missing type timerkit.Scheduler).
type Scheduler struct{}

// Module запускает все job'ы группы Group в fx-lifecycle приложения.
var Module = fx.Module(
	"timerkit",
	fx.Supply(Scheduler{}),
	fx.Invoke(startJobs),
)

// Provide регистрирует конструктор Job (или []Job — группа собирается через
// flatten) в группу Group и требует Module в графе. Конструктор неверного типа
// и граф без Module отвергаются при сборке: иначе job молча не запускается.
// Дополнительные аннотации (например fx.ParamTags) применяются до групповой;
// свой fx.ResultTags передавать нельзя.
func Provide(ctor any, anns ...fx.Annotation) fx.Option {
	tag, err := groupTag(ctor)
	if err != nil {
		return fx.Error(err)
	}
	return fx.Options(
		fx.Provide(fx.Annotate(ctor, append(anns, fx.ResultTags(tag))...)),
		fx.Invoke(requireModule),
	)
}

func requireModule(Scheduler) {}

var (
	jobType  = reflect.TypeFor[Job]()
	jobsType = reflect.TypeFor[[]Job]()
)

func groupTag(ctor any) (string, error) {
	t := reflect.TypeOf(ctor)
	if t == nil || t.Kind() != reflect.Func {
		return "", fmt.Errorf("timerkit: Provide expects a constructor function returning timerkit.Job or []timerkit.Job, got %T", ctor)
	}
	if t.NumOut() > 0 {
		switch t.Out(0) {
		case jobType:
			return `group:"` + Group + `"`, nil
		case jobsType:
			return `group:"` + Group + `,flatten"`, nil
		}
	}
	return "", fmt.Errorf("timerkit: Provide constructor must return timerkit.Job or []timerkit.Job as its first result, got %v", t)
}

type params struct {
	fx.In
	Jobs  []Job          `group:"timerkit.jobs"`
	Clock clockkit.Clock `optional:"true"`
}

func startJobs(lc fx.Lifecycle, p params, log logkit.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	r := newRunner(log, p.Clock)

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
