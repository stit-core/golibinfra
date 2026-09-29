package logkit

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/fx"
)

func TestModule_WithoutProviderStillBuilds(t *testing.T) {
	var got Logger
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Silent: true}),
		Module,
		fx.Populate(&got),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if got == nil {
		t.Fatal("Logger not populated")
	}
	got.Info(context.Background(), "ok")
}

func TestModule_PicksOptionalProviderAndRedact(t *testing.T) {
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	var got Logger
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Silent: true}, RedactConfig{}),
		fx.Provide(func() log.LoggerProvider { return lp }),
		Module,
		fx.Populate(&got),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	got.Info(context.Background(), "hello", "email", "x@y.io")

	if a := attrsOf(exp.only(t)); a["email"].AsString() != "[REDACTED]" {
		t.Errorf("redact not applied via module: %v", a)
	}
}

// KDOC-01: граф без RedactConfig маскирует дефолтным набором — умолчание держит
// код, а не провайдер, которого в графе может не быть.
func TestModule_MasksWithoutRedactConfig(t *testing.T) {
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })

	var got Logger
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Silent: true}),
		fx.Provide(func() log.LoggerProvider { return lp }),
		Module,
		fx.Populate(&got),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	got.Info(context.Background(), "login", "password", "hunter2", "authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.e30.c2ln")

	a := attrsOf(exp.only(t))
	if a["password"].AsString() != "[REDACTED]" || a["authorization"].AsString() != "[REDACTED]" {
		t.Fatalf("a graph without RedactConfig must mask: %v", a)
	}
}

func TestModule_ProvidesSlogAndLoggerFromOneChain(t *testing.T) {
	var (
		got Logger
		sl  *slog.Logger
	)
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Silent: true}),
		Module,
		fx.Populate(&got, &sl),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	if got == nil || sl == nil {
		t.Fatal("both Logger and *slog.Logger must be provided")
	}
	if !got.Enabled(context.Background(), slog.LevelInfo) || got.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Logger.Enabled must follow LOG_LEVEL of the graph config")
	}
}

func TestModule_InvalidRedactConfigFailsGraph(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Silent: true}, RedactConfig{Patterns: Patterns{`(`}}),
		Module,
		fx.Invoke(func(Logger) {}),
	)
	if app.Err() == nil {
		t.Fatal("fx.New must fail on an invalid redact pattern")
	}
}

func TestModule_InvalidConfigFailsGraph(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "nope"}),
		Module,
		fx.Invoke(func(Logger) {}),
	)
	if app.Err() == nil {
		t.Fatal("fx.New must fail on invalid LOG_LEVEL")
	}
}
