// Package telemetry — opt-in bootstrap OTel SDK: tracer/meter/logger providers,
// OTLP HTTP-экспортёры трёх сигналов (опционально с Bearer-токеном), resource-
// атрибуты service.name/service.version/deployment.environment.name, W3C
// propagator и runtime-инструментация. Без этого кита инструментация остальных
// китов инертна (no-op).
package telemetry

import (
	"context"
	"errors"
	"fmt"

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
)

type Providers struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
}

// Shutdown гасит провайдеры в порядке tracer → meter → logger: логи последними,
// чтобы ошибки остановки других сигналов ещё могли быть записаны. Ошибки
// собираются, остановка не прерывается.
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	if p.TracerProvider != nil {
		if err := p.TracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider: %w", err))
		}
	}
	if p.MeterProvider != nil {
		if err := p.MeterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter provider: %w", err))
		}
	}
	if p.LoggerProvider != nil {
		if err := p.LoggerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("logger provider: %w", err))
		}
	}
	return errors.Join(errs...)
}

func Init(ctx context.Context, cfg Config) (*Providers, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Disabled {
		p := &Providers{
			TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())),
			MeterProvider:  sdkmetric.NewMeterProvider(),
			LoggerProvider: sdklog.NewLoggerProvider(),
		}
		setGlobals(p)
		return p, nil
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
	}
	if cfg.Tenant != "" {
		attrs = append(attrs, semconv.ServiceNamespace(cfg.Tenant), attribute.String("tenant", cfg.Tenant))
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	p := &Providers{}
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
	setGlobals(p)
	return p, nil
}

func setGlobals(p *Providers) {
	otel.SetTracerProvider(p.TracerProvider)
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

func otlpHeaders(cfg Config) map[string]string {
	if cfg.OTLPBearerToken == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + cfg.OTLPBearerToken}
}
