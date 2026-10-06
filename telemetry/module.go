package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/log"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module поднимает SDK и гасит его на OnStop. Config он не provide'ит — caller
// регистрирует telemetry.NewConfig и logkit.NewConfig сам. Инструменты киты
// берут у глобальных провайдеров (otel.Tracer/otel.Meter), поэтому в граф
// уходят только *Providers и log.LoggerProvider (второй sink для logkit).
//
// fx.Invoke обязателен: без него конструктор ленив, и граф без потребителя
// *Providers стартовал бы с no-op инструментацией всех китов и без Shutdown.
var Module = fx.Module(
	"telemetry",
	fx.Provide(
		newProvidersWithLifecycle,
		func(p *Providers) log.LoggerProvider { return p.LoggerProvider },
	),
	fx.Invoke(func(*Providers) {}),
)

// NewConfig читает Config из ENV и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

type providerParams struct {
	fx.In

	LC     fx.Lifecycle
	Cfg    Config
	LogCfg logkit.Config
	Redact logkit.RedactConfig `optional:"true"`
}

func newProvidersWithLifecycle(p providerParams) (*Providers, error) {
	// Логгер строится из конфига, а не берётся из графа: logkit.Logger несёт
	// OTel-sink, и ошибка экспорта логов породила бы новую запись и новую ошибку.
	// Redact при этом сохраняется: в текст ошибки экспорта попадает URL
	// коллектора и произвольные атрибуты сообщений SDK.
	l, err := logkit.NewSlog(p.LogCfg, logkit.WithRedact(p.Redact))
	if err != nil {
		return nil, fmt.Errorf("telemetry: error logger: %w", err)
	}
	providers, err := Init(context.Background(), p.Cfg, WithErrorLogger(l.Handler()))
	if err != nil {
		return nil, err
	}
	p.LC.Append(fx.Hook{OnStop: providers.Shutdown})
	return providers, nil
}
