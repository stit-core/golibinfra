package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/authnkit/session/pgstore"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// scheduler — timerkit.Module с логгером: cleanup-job кита без него не собирается.
func scheduler() fx.Option {
	return fx.Options(fx.Provide(logkit.Nop), timerkit.Module)
}

func TestModule_ProvidesStore(t *testing.T) {
	t.Parallel()
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, testConfig),
		scheduler(),
		pgstore.Module,
		fx.Invoke(func(session.Store) {}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

func TestCleanupJob(t *testing.T) {
	t.Parallel()
	job := pgstore.CleanupJob(nil, testConfig)
	if job.Name != "authnkit.session.cleanup" || job.Interval != testConfig.CleanupInterval || job.Run == nil {
		t.Fatalf("CleanupJob = %+v", job)
	}
}

func TestModule_RequiresTimerkit(t *testing.T) {
	t.Parallel()
	err := fx.ValidateApp(fx.NopLogger, fx.Supply(&pgkit.DB{}, testConfig), pgstore.Module)
	if err == nil || !strings.Contains(err.Error(), "timerkit.scheduler") {
		t.Fatalf("pgstore.Module without timerkit.Module: err = %v, want missing timerkit.scheduler", err)
	}
}

func TestModule_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	app := fx.New(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}, session.Config{}),
		scheduler(),
		pgstore.Module,
		fx.Invoke(func(session.Store) {}),
	)
	if err := app.Err(); !errors.Is(err, session.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}

// Без миграций кита старт падает с подсказкой; с pgkit.MigrateModule проверка
// схемы идёт после миграций, в каком бы порядке ни стояли модули.
func TestModule_ChecksSchemaOnStart(t *testing.T) {
	pg := testkit.StartPostgres(t)
	app := func(extra ...fx.Option) *fx.App {
		return fx.New(append([]fx.Option{
			fx.NopLogger,
			fx.Supply(pg.PoolConfig(), testConfig),
			scheduler(),
			pgstore.Module,
			pgkit.Module,
		}, extra...)...)
	}

	bare := app()
	err := bare.Start(t.Context())
	if !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), `source "authnkit_session"`) {
		t.Fatalf("Start without migrations = %v, want pgkit.ErrSchema naming the source", err)
	}
	_ = bare.Stop(context.Background())

	migrated := app(fx.Supply(pgkit.MigrateRunnerConfig{Timeout: time.Minute, LockTimeout: time.Minute}), pgkit.MigrateModule)
	if err := migrated.Start(t.Context()); err != nil {
		t.Fatalf("Start with pgkit.MigrateModule: %v", err)
	}
	if err := migrated.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
