// Package telemetry — bootstrap OTel SDK: tracer/meter/logger providers,
// OTLP HTTP-экспортёры (с Bearer-токеном для корп ingest) или
// stdout-fallback для local-dev. Выставляет resource-атрибуты
// service.name, service.version, deployment.environment.name из Config;
// tenant/service.namespace выставляются только если Config.Tenant
// задан (типично local-dev) — в проде их force-сетит корп
// otel-collector из X-Scope-OrgID (корп obs-контракт). Включает стандартную
// runtime-инструментацию (go_*, process_*) через contrib/instrumentation/runtime.
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
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

type Providers struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	LoggerProvider *sdklog.LoggerProvider
}

func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	if p.LoggerProvider != nil {
		if err := p.LoggerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("logger provider: %w", err))
		}
	}
	if p.MeterProvider != nil {
		if err := p.MeterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter provider: %w", err))
		}
	}
	if p.TracerProvider != nil {
		if err := p.TracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider: %w", err))
		}
	}
	return errors.Join(errs...)
}

func Init(ctx context.Context, cfg Config) (*Providers, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironmentName(cfg.Environment),
	}
	if cfg.Tenant != "" {
		attrs = append(
			attrs,
			semconv.ServiceNamespace(cfg.Tenant),
			attribute.String("tenant", cfg.Tenant),
		)
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL, attrs...,
	))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	if cfg.StdoutOnly {
		return initStdout(res)
	}

	tp, err := newTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	mp, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	lp, err := newLoggerProvider(ctx, cfg, res)
	if err != nil {
		_ = mp.Shutdown(ctx)
		_ = tp.Shutdown(ctx)
		return nil, err
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Стандартный набор go-runtime метрик (runtime.go.goroutines,
	// runtime.go.mem.heap_alloc/inuse, runtime.go.gc.duration, etc.).
	// Без них невозможно отличить memory leak от utilization spike.
	if err := runtimeinstr.Start(
		runtimeinstr.WithMeterProvider(mp),
		runtimeinstr.WithMinimumReadMemStatsInterval(cfg.ExportTimeout),
	); err != nil {
		_ = lp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: runtime instrumentation: %w", err)
	}

	return &Providers{TracerProvider: tp, MeterProvider: mp, LoggerProvider: lp}, nil
}

func newTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(buildOTLPURL(cfg, "v1/traces"))}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(h))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(cfg.ExportTimeout)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	), nil
}

func newMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(buildOTLPURL(cfg, "v1/metrics"))}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(h))
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.ExportTimeout))),
		sdkmetric.WithResource(res),
	), nil
}

func newLoggerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	opts := []otlploghttp.Option{otlploghttp.WithEndpointURL(buildOTLPURL(cfg, "v1/logs"))}
	if h := otlpHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(h))
	}
	exp, err := otlploghttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: log exporter: %w", err)
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(res),
	), nil
}

func otlpHeaders(cfg Config) map[string]string {
	if cfg.OTLPBearerToken == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + cfg.OTLPBearerToken}
}

// buildOTLPURL составляет полный URL для OTLP-exporter'а с явной схемой —
// чтобы не зависеть от env-based scheme inference. У otlploghttp v0.19
// (experimental) есть quirk: `url.Parse("host:port")` парсит как
// scheme=host, opaque=port → insecureFromScheme считает scheme непустой
// → невольно ставит insecure=true. Явный URL обходит эту цепочку.
func buildOTLPURL(cfg Config, signalPath string) string {
	scheme := "https"
	if cfg.OTLPInsecure {
		scheme = "http"
	}
	return scheme + "://" + cfg.OTLPEndpoint + "/" + signalPath
}

// initStdout — dev-режим: все три сигнала идут в stdout через OTel
// stdout-exporters. SimpleProcessor / SimpleSpanProcessor — flush на
// каждую запись (для dev удобно: видим сразу, без 5s батча). Глобальные
// providers + propagator выставляются как в OTLP-ветке — чтобы
// otelhttp / otelpgx писали в наш stdout pipeline, а не в no-op default.
func initStdout(res *resource.Resource) (*Providers, error) {
	tExp, err := stdouttrace.New()
	if err != nil {
		return nil, fmt.Errorf("telemetry stdout: trace exporter: %w", err)
	}
	mExp, err := stdoutmetric.New()
	if err != nil {
		return nil, fmt.Errorf("telemetry stdout: metric exporter: %w", err)
	}
	lExp, err := stdoutlog.New()
	if err != nil {
		return nil, fmt.Errorf("telemetry stdout: log exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(tExp)),
		sdktrace.WithResource(res),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mExp)),
		sdkmetric.WithResource(res),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewSimpleProcessor(lExp)),
		sdklog.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Providers{TracerProvider: tp, MeterProvider: mp, LoggerProvider: lp}, nil
}
