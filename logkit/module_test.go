package logkit

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.uber.org/fx"
)

func TestModule_WithoutProviderStillBuilds(t *testing.T) {
	var got Logger
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Level: "info", Stdout: false}),
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
		fx.Supply(Config{Level: "info", Stdout: false}, RedactConfig{Enabled: true}),
		fx.Provide(func() log.LoggerProvider { return lp }),
		Module,
		fx.Populate(&got),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	got.Info(context.Background(), "hello", "email", "x@y.io")

	recs := exp.records()
	if len(recs) != 1 {
		t.Fatalf("otel records = %d, want 1", len(recs))
	}
	if a := attrsOf(recs[0]); a["email"].AsString() != "[REDACTED]" {
		t.Errorf("redact not applied via module: %v", a)
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
