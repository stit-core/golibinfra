package telemetry

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestModule_ProvidesLoggerProviderAndShutsDownOnStop(t *testing.T) {
	restoreGlobals(t)

	col, url := newCollector(t)
	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    url,
		SampleRatio:     1,
		ExportTimeout:   5 * time.Second,
		TraceBatchDelay: time.Hour,
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour, // только Shutdown может слить очередь
	}

	var lp log.LoggerProvider
	var p *Providers
	app := fxtest.New(t,
		fx.Supply(cfg, logkit.Config{Level: "info"}),
		Module,
		fx.Populate(&lp, &p),
	)
	app.RequireStart()

	if lp == nil || lp != log.LoggerProvider(p.LoggerProvider) {
		t.Fatal("Module must provide the LoggerProvider from Init (logkit picks it up as a second sink)")
	}
	var rec log.Record
	rec.SetBody(attribute.StringValue("hello"))
	lp.Logger("test").Emit(context.Background(), rec)

	app.RequireStop()

	col.mu.Lock()
	defer col.mu.Unlock()
	// Регрессия: OnStop обязан звать Shutdown, иначе последний батч логов теряется.
	if !slices.Contains(col.paths, "/v1/logs") {
		t.Fatalf("OnStop did not flush logs; collector saw %v", col.paths)
	}
}

// Регрессия: fx.Provide ленив. Без fx.Invoke граф, который не тянет *Providers
// (сервис со своей проводкой логов), стартовал бы с no-op инструментацией всех
// китов, невыставленным propagator'ом и без Shutdown на OnStop.
func TestModule_InitsSDKWithoutProvidersConsumer(t *testing.T) {
	restoreGlobals(t)

	col, url := newCollector(t)
	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    url,
		SampleRatio:     1,
		ExportTimeout:   5 * time.Second,
		TraceBatchDelay: time.Hour, // только Shutdown может слить очередь
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour,
	}

	app := fxtest.New(t,
		fx.Supply(cfg, logkit.Config{Level: "info", Silent: true}),
		Module,
	)
	app.RequireStart()

	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("global tracer provider = %T, want the SDK one", otel.GetTracerProvider())
	}
	if !slices.Contains(otel.GetTextMapPropagator().Fields(), "traceparent") {
		t.Fatalf("propagator fields = %v, want traceparent", otel.GetTextMapPropagator().Fields())
	}
	_, span := otel.Tracer("test").Start(context.Background(), "job")
	span.End()

	app.RequireStop()

	col.mu.Lock()
	defer col.mu.Unlock()
	if !slices.Contains(col.paths, "/v1/traces") {
		t.Fatalf("OnStop did not flush traces; collector saw %v", col.paths)
	}
}

// Регрессия: логгер ошибок экспорта строится внутри Module и остаётся
// единственным каналом логов, который redact обходил бы стороной. В текст
// ошибки попадают URL коллектора и атрибуты сообщений SDK.
func TestModule_RedactsErrorLogger(t *testing.T) {
	restoreGlobals(t)

	stdout := captureStdout(t)
	app := fxtest.New(t,
		fx.Supply(
			Config{Disabled: true},
			logkit.Config{Level: "info"},
			logkit.RedactConfig{Enabled: true},
		),
		Module,
	)
	app.RequireStart()
	otel.Handle(errors.New(`Post "https://collector/v1/traces": user john@example.com rejected`))
	app.RequireStop()

	got := stdout()
	if strings.Contains(got, "john@example.com") {
		t.Fatalf("PII survived in the export-error log: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("export-error log went out unredacted: %s", got)
	}
}

// captureStdout подменяет os.Stdout: логгер ошибок экспорта Module строит сам,
// снаружи его writer не задать.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })
	return func() string {
		os.Stdout = old
		_ = w.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read stdout: %v", err)
		}
		return string(b)
	}
}

// Регрессия: Module не занимает в графе metric.Meter и trace.Tracer — их
// провайдит потребитель (или берёт у глобальных провайдеров напрямую).
func TestModule_DoesNotProvideMeterOrTracer(t *testing.T) {
	restoreGlobals(t)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{Disabled: true}, logkit.Config{Level: "info"}),
		Module,
		fx.Provide(
			func() metric.Meter { return otel.Meter("app") },
			func() trace.Tracer { return otel.Tracer("app") },
		),
		fx.Invoke(func(metric.Meter, trace.Tracer) {}),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("consumer must be free to provide its own Meter/Tracer: %v", err)
	}
}

// Невалидный конфиг валит сборку графа, а не первый экспорт.
func TestModule_FailsFastOnInvalidConfig(t *testing.T) {
	restoreGlobals(t)

	tests := map[string]fx.Option{
		"telemetry": fx.Supply(Config{OTLPEndpoint: "localhost:4318"}, logkit.Config{Level: "info"}),
		"logkit":    fx.Supply(Config{Disabled: true}, logkit.Config{Level: "nonsense"}),
	}
	for name, supply := range tests {
		t.Run(name, func(t *testing.T) {
			app := fx.New(fx.NopLogger, supply, Module, fx.Invoke(func(*Providers) {}))
			if err := app.Err(); err == nil {
				t.Fatal("want an error from fx.New")
			}
		})
	}
}
