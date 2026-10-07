package flagkit

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestNewConfig_ReadsFeatureFlags(t *testing.T) {
	t.Setenv("FEATURE_FLAGS", "foo=true,region=eu")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Definitions != "foo=true,region=eu" {
		t.Fatalf("Definitions = %q, want the raw FEATURE_FLAGS value", cfg.Definitions)
	}
}

func TestNewConfig_EmptyEnvIsValid(t *testing.T) {
	t.Setenv("FEATURE_FLAGS", "")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Definitions != "" {
		t.Fatalf("Definitions = %q, want empty", cfg.Definitions)
	}
}

func TestNewConfig_RejectsInvalidDefinitions(t *testing.T) {
	t.Setenv("FEATURE_FLAGS", "new_checkout")

	if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestModule_ProvidesStore(t *testing.T) {
	var store Store
	app := fxtest.New(t,
		fx.Supply(Config{Definitions: "foo=true,region=eu"}),
		Module,
		fx.Populate(&store),
	)
	app.RequireStart().RequireStop()

	if !store.Bool("foo") || store.String("region") != "eu" {
		t.Fatalf("store = %v/%q, want true/\"eu\"", store.Bool("foo"), store.String("region"))
	}
}

// Регрессия KFX-03: мусор в FEATURE_FLAGS валит сборку графа и без потребителя Store.
func TestModule_InvalidConfigFailsGraph(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Definitions: "new_checkout"}),
		Module,
	)

	err := app.Err()
	if err == nil || !strings.Contains(err.Error(), ErrConfig.Error()) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}
