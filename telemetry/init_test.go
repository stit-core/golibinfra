package telemetry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	runtimeinstr "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

func TestInitDisabled_StillInstallsPropagatorAndGlobals(t *testing.T) {
	restoreGlobals(t)

	p, err := Init(t.Context(), Config{Disabled: true})
	if err != nil {
		t.Fatalf("Init(disabled) error: %v", err)
	}
	if p.TracerProvider == nil || p.MeterProvider == nil || p.LoggerProvider == nil {
		t.Fatal("Init(disabled) must return non-nil providers")
	}
	fields := otel.GetTextMapPropagator().Fields()
	if !slices.Contains(fields, "traceparent") || !slices.Contains(fields, "baggage") {
		t.Fatalf("propagator fields = %v, want traceparent+baggage", fields)
	}
	if global.GetLoggerProvider() != log.LoggerProvider(p.LoggerProvider) {
		t.Fatal("global logger provider must be the one from Init")
	}
	_, span := otel.Tracer("test").Start(t.Context(), "job")
	if span.IsRecording() {
		t.Fatal("disabled telemetry must not record spans")
	}
	span.End()
	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(disabled) error: %v", err)
	}
}

func TestInitRequiresServiceName(t *testing.T) {
	if _, err := Init(t.Context(), Config{OTLPEndpoint: "http://localhost:4318"}); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewResource_Attributes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		want    map[attribute.Key]string
		absent  []attribute.Key
		require bool
	}{
		{
			name: "service identity",
			cfg:  Config{ServiceName: "svc", ServiceVersion: "v1.2.3", Environment: "prod"},
			want: map[attribute.Key]string{
				semconv.ServiceNameKey:               "svc",
				semconv.ServiceVersionKey:            "v1.2.3",
				semconv.DeploymentEnvironmentNameKey: "prod",
			},
			absent: []attribute.Key{semconv.ServiceNamespaceKey, attribute.Key("tenant")},
		},
		{
			name: "tenant for local dev",
			cfg:  Config{ServiceName: "svc", ServiceVersion: "dev", Environment: "dev", Tenant: "acme"},
			want: map[attribute.Key]string{
				semconv.ServiceNameKey:      "svc",
				semconv.ServiceNamespaceKey: "acme",
				attribute.Key("tenant"):     "acme",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := newResource(tt.cfg)
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			got := map[attribute.Key]string{}
			for _, kv := range res.Attributes() {
				got[kv.Key] = kv.Value.String()
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
			for _, k := range tt.absent {
				if _, ok := got[k]; ok {
					t.Errorf("%s must not be set: %q", k, got[k])
				}
			}
		})
	}
}

// Регрессия: расхождение schema URL между semconv кита и builtin-детекторами
// SDK не должно валить старт сервиса.
func TestMergeResource_ToleratesSchemaURLConflict(t *testing.T) {
	t.Parallel()

	a := resource.NewWithAttributes("https://opentelemetry.io/schemas/1.40.0", attribute.String("a", "1"))
	b := resource.NewWithAttributes("https://opentelemetry.io/schemas/1.41.0", attribute.String("b", "2"))

	res, err := mergeResource(a, b)
	if err != nil {
		t.Fatalf("mergeResource: %v", err)
	}
	if res == nil {
		t.Fatal("mergeResource must return a usable resource")
	}
	got := map[attribute.Key]string{}
	for _, kv := range res.Attributes() {
		got[kv.Key] = kv.Value.String()
	}
	if got["a"] != "1" || got["b"] != "2" {
		t.Fatalf("attributes lost: %v", got)
	}
	if res.SchemaURL() != "" {
		t.Fatalf("schema URL on conflict = %q, want empty", res.SchemaURL())
	}
}

// Каждый провайдер проверяет endpoint сам: Config.Validate собирает URL только
// для трейсов, а per-signal путь дописывает уже конструктор.
func TestProviderConstructors_RejectEndpointWithoutScheme(t *testing.T) {
	t.Parallel()

	cfg := Config{ServiceName: "svc", OTLPEndpoint: "localhost:4318", ExportTimeout: time.Second}
	res, err := newResource(cfg)
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}
	tests := map[string]func() error{
		"traces":  func() error { _, err := newTracerProvider(t.Context(), cfg, res); return err },
		"metrics": func() error { _, err := newMeterProvider(t.Context(), cfg, res); return err },
		"logs":    func() error { _, err := newLoggerProvider(t.Context(), cfg, res); return err },
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			if err := build(); !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
		})
	}
}

// Список runtime-метрик — часть README: тест ловит их пропажу или переименование
// при апгрейде contrib.
func TestRuntimeInstrumentation_EmittedInstruments(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := runtimeinstr.Start(runtimeinstr.WithMeterProvider(mp)); err != nil {
		t.Fatalf("runtime instrumentation: %v", err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rm.ScopeMetrics) != 1 {
		t.Fatalf("scope metrics: got %d, want 1", len(rm.ScopeMetrics))
	}
	if got := rm.ScopeMetrics[0].Scope.Name; got != "go.opentelemetry.io/contrib/instrumentation/runtime" {
		t.Fatalf("scope: got %q", got)
	}
	units := map[string]string{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		units[m.Name] = m.Unit
	}
	want := map[string]string{
		"go.memory.used":        "By",
		"go.memory.allocated":   "By",
		"go.memory.allocations": "{allocation}",
		"go.memory.gc.goal":     "By",
		"go.goroutine.count":    "{goroutine}",
		"go.processor.limit":    "{thread}",
		"go.config.gogc":        "%",
	}
	for name, unit := range want {
		got, ok := units[name]
		if !ok {
			t.Errorf("runtime metric %s is not emitted; got %v", name, slices.Sorted(maps.Keys(units)))
			continue
		}
		if got != unit {
			t.Errorf("%s unit = %q, want %q", name, got, unit)
		}
	}
	// go.memory.limit наблюдается только при заданном GOMEMLIMIT.
	for name := range units {
		if _, ok := want[name]; !ok && name != "go.memory.limit" {
			t.Errorf("undocumented runtime metric %s", name)
		}
	}
}

func TestShutdown_SplitsDeadlineBetweenSignals(t *testing.T) {
	t.Parallel()

	const total = 300 * time.Millisecond

	var budgets []time.Duration
	// Шаг ведёт себя как экспортёр при недоступном коллекторе: тратит весь бюджет.
	step := func(name string) shutdownStep {
		return shutdownStep{name: name, stop: func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Errorf("%s: step must get a deadline", name)
				return nil
			}
			budgets = append(budgets, time.Until(deadline))
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	p := &Providers{exportTimeout: 10 * time.Second, steps: []shutdownStep{step("tracer"), step("meter"), step("logger")}}

	ctx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()
	_ = p.Shutdown(ctx)

	if len(budgets) != 3 {
		t.Fatalf("steps run: got %d, want 3", len(budgets))
	}
	// Регрессия: без деления бюджета tracer забирал бы весь дедлайн, а logger —
	// последний, чтобы записать ошибки остановки, — получал бы истёкший ctx.
	for i, b := range budgets {
		if b < total/6 || b > total/2 {
			t.Fatalf("step %d budget = %s, want ~%s (deadline/3)", i, b, total/3)
		}
	}
}

func TestShutdown_ExpiredContextStillRunsEveryStep(t *testing.T) {
	t.Parallel()

	var ran []string
	step := func(name string) shutdownStep {
		return shutdownStep{name: name, stop: func(ctx context.Context) error {
			ran = append(ran, name)
			return ctx.Err()
		}}
	}
	p := &Providers{exportTimeout: time.Second, steps: []shutdownStep{step("tracer"), step("meter"), step("logger")}}

	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	err := p.Shutdown(ctx)
	if err == nil {
		t.Fatal("want joined errors from an expired context")
	}
	if !slices.Equal(ran, []string{"tracer", "meter", "logger"}) {
		t.Fatalf("steps run: %v", ran)
	}
	for _, name := range ran {
		if !slices.Contains([]string{"tracer", "meter", "logger"}, name) {
			t.Fatalf("unexpected step %q", name)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if !strings.HasPrefix(err.Error(), "telemetry: shutdown tracer: ") {
		t.Fatalf("err = %v, want the telemetry prefix and the step", err)
	}
}

func TestShutdown_NilProvidersAreSkipped(t *testing.T) {
	t.Parallel()

	if err := (&Providers{}).Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown of empty Providers: %v", err)
	}
}

type collector struct {
	mu    sync.Mutex
	paths []string
	auth  map[string]bool
	org   map[string]bool
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.auth[r.Header.Get("Authorization")] = true
	c.org[r.Header.Get("X-Scope-OrgID")] = true
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func newCollector(t *testing.T) (*collector, string) {
	t.Helper()
	col := &collector{auth: map[string]bool{}, org: map[string]bool{}}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)
	return col, srv.URL
}

func TestInit_ExportsAllSignalsToSpecPaths(t *testing.T) {
	restoreGlobals(t)

	col, url := newCollector(t)
	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    url + "/base/",
		OTLPHeaders:     Headers{"x-scope-orgid": "acme"},
		OTLPBearerToken: "tok",
		SampleRatio:     1,
		ExportTimeout:   5 * time.Second,
		TraceBatchDelay: time.Hour,
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour,
	}
	p, err := Init(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, span := otel.Tracer("test").Start(context.Background(), "job")
	span.End()
	var rec log.Record
	rec.SetBody(attribute.StringValue("hello"))
	global.GetLoggerProvider().Logger("test").Emit(context.Background(), rec)

	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	for _, want := range []string{"/base/v1/traces", "/base/v1/metrics", "/base/v1/logs"} {
		if !slices.Contains(col.paths, want) {
			t.Errorf("collector never saw %s; got %v", want, col.paths)
		}
	}
	if !col.auth["Bearer tok"] || len(col.auth) != 1 {
		t.Errorf("auth headers = %v, want only Bearer tok", col.auth)
	}
	if !col.org["acme"] || len(col.org) != 1 {
		t.Errorf("X-Scope-OrgID = %v, want only acme", col.org)
	}
}

// Регрессия ROBS-02: выключенный SDK не трогает trace-context. Спан поверх входящего контекста
// несёт его вниз как пришёл — с тем же решением о сэмплировании, — а спан без родителя
// контекста не порождает: нижестоящий сервис не получает traceparent с sampled=0 и пишет свою
// трассу сам.
func TestInitDisabled_LeavesTraceContextToNeighbours(t *testing.T) {
	restoreGlobals(t)
	if _, err := Init(t.Context(), Config{Disabled: true}); err != nil {
		t.Fatalf("Init(disabled) error: %v", err)
	}
	const sampled, dropped = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	for name, c := range map[string]struct{ in, out string }{
		"sampled parent":   {in: sampled, out: sampled},
		"unsampled parent": {in: dropped, out: dropped},
		"no parent":        {},
	} {
		t.Run(name, func(t *testing.T) {
			prop := otel.GetTextMapPropagator()
			ctx := t.Context()
			if c.in != "" {
				ctx = prop.Extract(ctx, propagation.MapCarrier{"traceparent": c.in})
			}
			ctx, handle := otel.Tracer("kit").Start(ctx, "handle")
			defer handle.End()
			ctx, call := otel.Tracer("kit").Start(ctx, "call")
			defer call.End()
			carrier := propagation.MapCarrier{}
			prop.Inject(ctx, carrier)
			if got := carrier["traceparent"]; got != c.out {
				t.Fatalf("outgoing traceparent = %q, want %q: a disabled SDK must not rewrite the trace context", got, c.out)
			}
		})
	}
}

// Регрессия ROBS-02: tracer, созданный до Init (пакетная переменная потребителя, кит, собранный
// раньше telemetry.Module), при выключенном SDK ведёт себя как созданный после: делегат OTel
// привязывает его к первому провайдеру трасс процесса, и это no-op, а не SDK-провайдер с
// sampled=0. Свой процесс: делегирование срабатывает один раз на процесс.
func TestInitDisabled_TracerCreatedBeforeInitIsNoop(t *testing.T) {
	const (
		child   = "TELEMETRY_EARLY_TRACER_CHILD"
		sampled = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	)
	if os.Getenv(child) == "1" {
		early := otel.Tracer("early")
		if _, err := Init(context.Background(), Config{Disabled: true}); err != nil {
			fmt.Print("init: ", err)
			return
		}
		prop := otel.GetTextMapPropagator()
		for _, tracer := range []trace.Tracer{early, otel.Tracer("late")} {
			for _, in := range []string{sampled, ""} {
				ctx := prop.Extract(context.Background(), propagation.MapCarrier{"traceparent": in})
				ctx, span := tracer.Start(ctx, "call")
				out := propagation.MapCarrier{}
				prop.Inject(ctx, out)
				span.End()
				fmt.Printf("[%s]", out["traceparent"])
			}
		}
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInitDisabled_TracerCreatedBeforeInitIsNoop$")
	cmd.Env = append(os.Environ(), child+"=1")
	out, err := cmd.CombinedOutput()
	// Оба tracer'а: входящий контекст — вниз как пришёл, без входящего — заголовка нет.
	if want := "[" + sampled + "][][" + sampled + "][]"; err != nil || !strings.Contains(string(out), want) {
		t.Fatalf("early and late tracers must both leave the trace context alone, want %s; child: %v\n%s", want, err, out)
	}
}

// restoreGlobals возвращает глобалы OTel после теста. Error-handler
// восстанавливается тоже: иначе следующие тесты пакета отправляли бы ошибки
// SDK в буфер уже завершившегося теста. Симметричного Get у otel.SetLogger нет,
// поэтому канал внутренних сообщений просто глушится.
func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, mp, lp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), global.GetLoggerProvider(), otel.GetTextMapPropagator()
	eh := otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		global.SetLoggerProvider(lp)
		otel.SetTextMapPropagator(prop)
		otel.SetErrorHandler(eh)
		otel.SetLogger(logr.Discard())
	})
}
