package telemetry

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// Module не provide'ит Config — caller регистрирует его сам
// (fx.Provide(telemetry.NewConfig)). Отдаёт *Providers, trace.Tracer,
// metric.Meter и log.LoggerProvider (второй sink для logkit.Module).
var Module = fx.Module(
	"telemetry",
	fx.Provide(
		newProvidersWithLifecycle,
		func(*Providers) trace.Tracer { return otel.Tracer(tracerName) },
		func(*Providers) metric.Meter { return otel.Meter(tracerName) },
		func(p *Providers) log.LoggerProvider { return p.LoggerProvider },
	),
)

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func newProvidersWithLifecycle(lc fx.Lifecycle, cfg Config) (*Providers, error) {
	p, err := Init(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: p.Shutdown})
	return p, nil
}
