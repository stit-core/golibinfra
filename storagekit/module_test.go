package storagekit_test

import (
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
