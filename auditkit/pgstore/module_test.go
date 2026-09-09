package pgstore_test

import (
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/auditkit/pgstore"
	"git.stit.tech/stit-core/golibinfra/pgkit"
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
