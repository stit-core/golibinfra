package storagekit_test

import (
	"errors"
	"testing"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func TestModule_ProvidesURLBuilderFromConfig(t *testing.T) {
	cfg := validConfig()
	cfg.URLStyle = storagekit.URLStyleVirtual

	var urls *storagekit.URLBuilder
	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		storagekit.Module,
		fx.Populate(&urls),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if got := urls.Build("k.png"); got != "https://cdn.example.com/k.png" {
		t.Fatalf("Build = %q, want virtual style URL from Config", got)
	}
}

// Config мимо NewConfig (App-структура, fx.Supply) с опечаткой стиля и без эндпоинта — отказ
// сборки графа, а не URLBuilder из невалидного Config.
func TestModule_RejectsInvalidConfig(t *testing.T) {
	cfg := validConfig()
	cfg.URLStyle = "Virtual"
	cfg.Endpoint = ""
	err := fx.New(fx.NopLogger, fx.Supply(cfg), storagekit.Module, fx.Invoke(func(*storagekit.URLBuilder) {})).Err()
	if !errors.Is(err, storagekit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}
