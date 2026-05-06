package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

var Module = fx.Module("telemetry",
	fx.Provide(
		newProvidersWithLifecycle,
		newSloggerFromConfig,
		newTracer,
		newMeter,
	),
)

func newProvidersWithLifecycle(lc fx.Lifecycle, cfg Config) (*Providers, error) {
	ctx, cancel := context.WithCancel(context.Background())

	p, err := Init(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(stopCtx context.Context) error {
			defer cancel()
			return p.Shutdown(stopCtx)
		},
	})

	return p, nil
}

// Slogger зависит от Providers, чтобы инициализация OTel завершилась раньше.
func newSloggerFromConfig(_ *Providers) *slog.Logger {
	return NewSlogger(slog.LevelInfo)
}

func newTracer(_ *Providers) trace.Tracer { return otel.Tracer("app") }
func newMeter(_ *Providers) metric.Meter  { return otel.Meter("app") }
