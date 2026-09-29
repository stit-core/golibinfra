package logkit

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
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

// KDOC-01, KSEC-01 (эксперимент E01 базового аудита): граф из одного
// logkit.NewConfig без RedactConfig при пустом окружении маскирует оба sink'а —
// умолчание держит код, а не провайдер, которого в графе может не быть.
func TestModule_MasksWithoutRedactConfig(t *testing.T) {
	const secret = "S3cretPWzzcs"
	clearEnv(t, "LOG_LEVEL", "LOG_FORMAT", "LOG_SOURCE", "LOG_SILENT", "LOG_REDACT_DISABLED", "LOG_REDACT_PATTERNS", "LOG_REDACT_REPLACE_WITH")
	exp, lp := newExporter(t)

	var got Logger
	stdout := captureStdout(t, func() {
		app := fx.New(
			fx.NopLogger,
			fx.Provide(NewConfig),
			fx.Provide(func() log.LoggerProvider { return lp }),
			Module,
			fx.Populate(&got),
		)
		if err := app.Err(); err != nil {
			t.Fatalf("fx: %v", err)
		}
		got.Info(context.Background(), "login", "password", secret, "authorization", "Bearer "+secret)
	})

	if strings.Contains(stdout, secret) || !strings.Contains(stdout, "[REDACTED]") {
		t.Fatalf("stdout of a graph without RedactConfig must be masked: %s", stdout)
	}
	a := attrsOf(exp.only(t))
	if a["password"].AsString() != "[REDACTED]" || a["authorization"].AsString() != "[REDACTED]" {
		t.Fatalf("otel sink of a graph without RedactConfig must be masked: %v", a)
	}
}

// captureStdout подменяет os.Stdout на время fn: без WithWriter логгер пишет в
// os.Stdout, прочитанный при сборке.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	func() {
		defer func() { os.Stdout = orig }()
		fn()
	}()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(out)
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
