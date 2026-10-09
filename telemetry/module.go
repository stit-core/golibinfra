package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/log"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/internal/stopbudget"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module поднимает SDK и гасит его на OnStop за две трети остатка бюджета остановки: ошибка
// финального экспорта (коллектор недоступен или молчит) — запись ERROR логгера ошибок, а не
// ошибка остановки, — телеметрия best-effort. Config он не
// provide'ит — caller регистрирует telemetry.NewConfig и logkit.NewConfig сам. Инструменты киты
// берут у глобальных провайдеров (otel.Tracer/otel.Meter), поэтому в граф уходят только
// *Providers и log.LoggerProvider (второй sink для logkit).
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
	// коллектора и произвольные атрибуты сообщений SDK. Второго канала у него нет, поэтому при
	// LOG_SILENT его ERROR идут в stderr.
	l, err := logkit.NewSlog(p.LogCfg, logkit.WithRedact(p.Redact), logkit.WithStderrFallback())
	if err != nil {
		return nil, fmt.Errorf("telemetry: error logger: %w", err)
	}
	providers, err := Init(context.Background(), p.Cfg, WithErrorLogger(l.Handler()))
	if err != nil {
		return nil, err
	}
	p.LC.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		// Треть бюджета остановки — в запасе: хук, вернувшийся на самом дедлайне, fx считает
		// просроченным — молчащий коллектор дал бы ошибку остановки.
		ctx, cancel := stopbudget.Share(ctx)
		defer cancel()
		// Ошибка остановки — код выхода 1: Job без коллектора упал бы, хотя работа сделана.
		if err := providers.Shutdown(ctx); err != nil {
			l.ErrorContext(ctx, "telemetry: shutdown", "error", err)
		}
		return nil
	}})
	return providers, nil
}
