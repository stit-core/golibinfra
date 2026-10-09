// Package telemetry — opt-in bootstrap OTel SDK: провайдеры трёх сигналов,
// OTLP/HTTP-экспортёры, resource-атрибуты, W3C-propagator и runtime-метрики.
// Без этого кита инструментация остальных китов инертна (no-op).
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"time"

	runtimeinstr "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Providers — поднятые SDK-провайдеры трёх сигналов. При Config.Disabled они без экспортёров, а
// провайдер трасс процесса (otel.GetTracerProvider) — no-op: TracerProvider в глобальные не
// ставится.
type Providers struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider

	exportTimeout time.Duration
	// steps подменяются в тестах; nil — шаги строятся из полей выше.
	steps []shutdownStep
}

type shutdownStep struct {
	name string
	stop func(context.Context) error
}

// Shutdown гасит провайдеры в порядке tracer → meter → logger: логи последними,
// чтобы ошибки остановки других сигналов ещё могли быть записаны. Дедлайн ctx
// делится между шагами — иначе последний шаг получает уже истёкший контекст.
// Ошибки собираются, остановка не прерывается.
func (p *Providers) Shutdown(ctx context.Context) error {
	steps := p.steps
	if steps == nil {
		steps = p.defaultSteps()
	}
	var errs []error
	for i, s := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, p.stepBudget(ctx, len(steps)-i))
		err := s.stop(stepCtx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("telemetry: shutdown %s: %w", s.name, err))
		}
	}
	return errors.Join(errs...)
}

func (p *Providers) defaultSteps() []shutdownStep {
	var steps []shutdownStep
	if p.TracerProvider != nil {
		steps = append(steps, shutdownStep{"tracer provider", p.TracerProvider.Shutdown})
	}
	if p.MeterProvider != nil {
		steps = append(steps, shutdownStep{"meter provider", p.MeterProvider.Shutdown})
	}
	if p.LoggerProvider != nil {
		steps = append(steps, shutdownStep{"logger provider", p.LoggerProvider.Shutdown})
	}
	return steps
}

func (p *Providers) stepBudget(ctx context.Context, remaining int) time.Duration {
	budget := p.exportTimeout
	if budget <= 0 {
		budget = defaultExportTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		if share := time.Until(deadline) / time.Duration(remaining); share < budget {
			budget = share
		}
	}
	return budget
}

// Init поднимает SDK, выставляет глобальные провайдеры и propagator и
// возвращает Providers для остановки. Ошибки экспорта уходят в stderr, пока не
// передан WithErrorLogger.
func Init(ctx context.Context, cfg Config, opts ...Option) (*Providers, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	installErrorLogging(o.errorLog)

	if cfg.Disabled {
		p := &Providers{
			TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())),
			MeterProvider:  sdkmetric.NewMeterProvider(),
			LoggerProvider: sdklog.NewLoggerProvider(),
			exportTimeout:  cfg.ExportTimeout,
		}
		// Провайдер трасс процесса — no-op: спан несёт контекст родителя как есть. SDK-провайдер
		// и с NeverSample выдаёт спану свой контекст с sampled=0 — он ушёл бы вниз в traceparent,
		// и нижестоящий сервис перестал бы писать трассу, которую сэмплировал вышестоящий или
		// которую начал бы сам. Единственным: tracer, созданный до Init, делегат OTel
		// привязывает к первому провайдеру процесса.
		setGlobals(tracenoop.NewTracerProvider(), p)
		return p, nil
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, err
	}

	p := &Providers{exportTimeout: cfg.ExportTimeout}
	if p.TracerProvider, err = newTracerProvider(ctx, cfg, res); err != nil {
		return nil, err
	}
	if p.MeterProvider, err = newMeterProvider(ctx, cfg, res); err != nil {
		_ = p.Shutdown(ctx)
		return nil, err
	}
	if p.LoggerProvider, err = newLoggerProvider(ctx, cfg, res); err != nil {
		_ = p.Shutdown(ctx)
		return nil, err
	}
	if err := runtimeinstr.Start(
		runtimeinstr.WithMeterProvider(p.MeterProvider),
		runtimeinstr.WithMinimumReadMemStatsInterval(cfg.MetricInterval),
	); err != nil {
		_ = p.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: runtime instrumentation: %w", err)
	}
	setGlobals(p.TracerProvider, p)
	return p, nil
}

// newResource собирает resource-атрибуты сервиса поверх детекторов SDK.
func newResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
	}
	if cfg.Tenant != "" {
		attrs = append(attrs, semconv.ServiceNamespace(cfg.Tenant), attribute.String("tenant", cfg.Tenant))
	}
	return mergeResource(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
}

// mergeResource терпит ErrSchemaURLConflict: при расхождении semconv кита и
// builtin-детекторов SDK Merge возвращает пригодный ресурс без schema URL —
// это не повод не стартовать. Любая другая ошибка (и пустой результат) валит
// старт: сервис без resource-атрибутов не найти на дашбордах.
func mergeResource(base, extra *resource.Resource) (*resource.Resource, error) {
	res, err := resource.Merge(base, extra)
	if res != nil && (err == nil || errors.Is(err, resource.ErrSchemaURLConflict)) {
		return res, nil
	}
	return nil, fmt.Errorf("telemetry: resource: %w", err)
}

func setGlobals(tracer trace.TracerProvider, p *Providers) {
	otel.SetTracerProvider(tracer)
	otel.SetMeterProvider(p.MeterProvider)
	global.SetLoggerProvider(p.LoggerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

func newTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	u, err := signalURL(cfg.OTLPEndpoint, "v1/traces")
	if err != nil {
		return nil, err
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(u), otlptracehttp.WithTimeout(cfg.ExportTimeout)}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(h))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(
			exp,
			sdktrace.WithBatchTimeout(cfg.TraceBatchDelay),
			sdktrace.WithExportTimeout(cfg.ExportTimeout),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	), nil
}

func newMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	u, err := signalURL(cfg.OTLPEndpoint, "v1/metrics")
	if err != nil {
		return nil, err
	}
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(u), otlpmetrichttp.WithTimeout(cfg.ExportTimeout)}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(h))
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(
			exp,
			sdkmetric.WithInterval(cfg.MetricInterval),
			sdkmetric.WithTimeout(cfg.ExportTimeout),
		)),
		sdkmetric.WithResource(res),
	), nil
}

func newLoggerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	u, err := signalURL(cfg.OTLPEndpoint, "v1/logs")
	if err != nil {
		return nil, err
	}
	opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(u), otlploghttp.WithTimeout(cfg.ExportTimeout)}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(h))
	}
	exp, err := otlploghttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: log exporter: %w", err)
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(
			exp,
			sdklog.WithExportInterval(cfg.LogBatchDelay),
			sdklog.WithExportTimeout(cfg.ExportTimeout),
		)),
		sdklog.WithResource(res),
	), nil
}
