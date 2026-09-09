package pgstore_test

import (
	"errors"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/bruteforcekit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

func TestModule_ProvidesGuardAndCleaner(t *testing.T) {
	t.Parallel()
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, config()),
		pgstore.Module,
		fx.Invoke(func(bruteforcekit.Guard, bruteforcekit.Cleaner) {}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Невалидный конфиг валит старт приложения, а не остаётся молчаливым fail-open.
func TestModule_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	app := fx.New(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, bruteforcekit.Config{}),
		pgstore.Module,
		fx.Invoke(func(bruteforcekit.Guard) {}),
	)
	if err := app.Err(); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}

func TestModule_RegistersJobAndMigrations(t *testing.T) {
	t.Parallel()
	var (
		jobs    []timerkit.Job
		sources []pgkit.Source
	)
	app := fx.New(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, config()),
		pgstore.Module,
		fx.Populate(
			fx.Annotate(&jobs, fx.ParamTags(`group:"`+timerkit.Group+`"`)),
			fx.Annotate(&sources, fx.ParamTags(`group:"pgkit.migrations"`)),
		),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("app: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "bruteforcekit.cleanup" {
		t.Fatalf("timer jobs = %+v, want one bruteforcekit.cleanup", jobs)
	}
	if jobs[0].Interval != config().CleanupInterval {
		t.Fatalf("job interval = %v, want %v", jobs[0].Interval, config().CleanupInterval)
	}
	if len(sources) != 1 || sources[0].Name != "bruteforcekit" {
		t.Fatalf("migrations group = %+v, want one bruteforcekit source", sources)
	}
}
