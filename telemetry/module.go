package telemetry

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(telemetry.NewConfig)`.
var Module = fx.Module(
	"telemetry",
	fx.Provide(
		newProvidersWithLifecycle,
		newTracer,
		newMeter,
		extractLoggerProvider,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

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

func newTracer(_ *Providers) trace.Tracer { return otel.Tracer("app") }
func newMeter(_ *Providers) metric.Meter  { return otel.Meter("app") }

// extractLoggerProvider экспонирует *sdklog.LoggerProvider отдельно от
// Providers — чтобы logkit.New / logkit.NewSlog могли резолвить его через
// fx без явного доступа к struct. Lifecycle уже привязан в
// newProvidersWithLifecycle (init.go держит реальный newLoggerProvider).
func extractLoggerProvider(p *Providers) *sdklog.LoggerProvider { return p.LoggerProvider }
