package telemetry

import (
	"context"
	"errors"
	"io"
	"net"
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
// ошибки попадают URL коллектора и атрибуты сообщений SDK. Маскирует и граф без
// logkit.RedactConfig (KSEC-01 базового аудита).
func TestModule_RedactsErrorLogger(t *testing.T) {
	graphs := map[string]fx.Option{
		"с RedactConfig":   fx.Supply(logkit.RedactConfig{}),
		"без RedactConfig": fx.Options(),
	}
	for name, redact := range graphs {
		t.Run(name, func(t *testing.T) {
			restoreGlobals(t)

			stdout := captureStdout(t)
			app := fxtest.New(t,
				fx.Supply(Config{Disabled: true}, logkit.Config{Level: "info"}),
				redact,
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
		})
	}
}

// Регрессия KFX-05: недоступный коллектор на остановке — запись ERROR логгера ошибок, а Stop — без
// ошибки: телеметрия best-effort, код выхода сервиса и Job от коллектора не зависит.
func TestModule_StopWithCollectorDownIsBestEffort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	refused := "http://" + ln.Addr().String()
	_ = ln.Close()
	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    refused,
		SampleRatio:     1,
		ExportTimeout:   time.Second,
		TraceBatchDelay: time.Hour, // экспорт — только финальный, на остановке
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour,
	}

	// При LOG_SILENT=true второго канала у логгера ошибок нет: запись идёт в stderr, а не теряется.
	for name, c := range map[string]struct {
		silent bool
		stream **os.File
	}{
		"stdout":                 {false, &os.Stdout},
		"LOG_SILENT=true stderr": {true, &os.Stderr},
	} {
		t.Run(name, func(t *testing.T) {
			restoreGlobals(t)
			logged := captureStream(t, c.stream)
			app := fx.New(fx.NopLogger, fx.Supply(cfg, logkit.Config{Level: "info", Silent: c.silent}), Module)
			if err := app.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			_, span := otel.Tracer("test").Start(context.Background(), "job")
			span.End()
			if err := app.Stop(t.Context()); err != nil {
				t.Fatalf("Stop with the collector down = %v, want nil", err)
			}
			got := logged()
			if !strings.Contains(got, `"level":"ERROR","msg":"telemetry: shutdown"`) || !strings.Contains(got, "connection refused") {
				t.Fatalf("no ERROR record of the failed final export:\n%s", got)
			}
		})
	}
}

// Молчащий коллектор (соединение принято, ответа нет) держит финальный экспорт каждого сигнала до
// его дедлайна: остановка укладывается в бюджет и не возвращает ошибку — хук, дотянувший до
// дедлайна fx, был бы ошибкой остановки и кодом выхода 1.
func TestModule_StopWithSilentCollectorFitsBudget(t *testing.T) {
	restoreGlobals(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	held := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()
	t.Cleanup(func() {
		for len(held) > 0 {
			_ = (<-held).Close()
		}
	})
	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    "http://" + ln.Addr().String(),
		SampleRatio:     1,
		ExportTimeout:   10 * time.Second,
		TraceBatchDelay: time.Hour,
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour,
	}

	stderr := captureStream(t, &os.Stderr)
	var providers *Providers
	app := fx.New(fx.NopLogger, fx.Supply(cfg, logkit.Config{Level: "info", Silent: true}), Module, fx.Populate(&providers))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// У каждого сигнала есть что экспортировать: все три шага остановки упираются в коллектор.
	_, span := otel.Tracer("test").Start(context.Background(), "job")
	span.End()
	counter, err := otel.Meter("test").Int64Counter("jobs_total")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	counter.Add(context.Background(), 1)
	log, err := logkit.New(logkit.Config{Level: "info", Silent: true}, logkit.WithOTel(providers.LoggerProvider))
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	log.Info(context.Background(), "a record to flush")

	const budget = 900 * time.Millisecond
	stopCtx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	begin := time.Now()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop with a silent collector = %v after %s, want nil within the %s budget", err, time.Since(begin), budget)
	}
	if got := stderr(); strings.Count(got, "context deadline exceeded") < 3 || !strings.Contains(got, `"msg":"telemetry: shutdown"`) {
		t.Fatalf("every signal must have waited for the collector and the failure must be logged:\n%s", got)
	}
}

// captureStdout подменяет os.Stdout: логгер ошибок экспорта Module строит сам,
// снаружи его writer не задать.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	return captureStream(t, &os.Stdout)
}

func captureStream(t *testing.T, stream **os.File) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := *stream
	*stream = w
	t.Cleanup(func() { *stream = old })
	return func() string {
		*stream = old
		_ = w.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read stream: %v", err)
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
