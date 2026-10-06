package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestModule_ProvidesLogger(t *testing.T) {
	t.Parallel()
	err := fx.ValidateApp(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}),
		pgstore.Module,
		fx.Invoke(func(auditkit.Logger) {}),
	)
	if err != nil {
		t.Fatalf("ValidateApp: %v", err)
	}
}

// Миграции кита должны попадать в группу pgkit.MigrateModule.
func TestModule_RegistersMigrations(t *testing.T) {
	t.Parallel()
	var sources []pgkit.Source
	app := fx.New(
		fx.NopLogger,
		fx.Supply(&pgkit.DB{}),
		pgstore.Module,
		fx.Populate(fx.Annotate(&sources, fx.ParamTags(`group:"pgkit.migrations"`))),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("app: %v", err)
	}
	if len(sources) != 1 || sources[0].Name != "auditkit" {
		t.Fatalf("migrations group = %+v, want one auditkit source", sources)
	}
}

// Без миграций кита старт падает с именем источника, а не первая запись аудита (KX62); с
// pgkit.MigrateModule проверка идёт после миграций.
func TestModule_ChecksSchemaOnStart(t *testing.T) {
	pg := testkit.StartPostgres(t)
	app := func(extra ...fx.Option) *fx.App {
		return fx.New(append([]fx.Option{
			fx.NopLogger,
			pgstore.Module,
			fx.Supply(pg.PoolConfig()),
			fx.Provide(logkit.Nop),
			pgkit.Module,
			fx.Invoke(func(auditkit.Logger) {}),
		}, extra...)...)
	}

	bare := app()
	err := bare.Start(t.Context())
	_ = bare.Stop(context.Background())
	if !errors.Is(err, pgkit.ErrSchema) || !strings.Contains(err.Error(), `source "auditkit"`) {
		t.Fatalf("Start without migrations = %v, want pgkit.ErrSchema naming the source", err)
	}

	migrated := app(fx.Supply(pgkit.MigrateRunnerConfig{Timeout: time.Minute, LockTimeout: time.Minute}), pgkit.MigrateModule)
	if err := migrated.Start(t.Context()); err != nil {
		t.Fatalf("Start with pgkit.MigrateModule: %v", err)
	}
	if err := migrated.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
