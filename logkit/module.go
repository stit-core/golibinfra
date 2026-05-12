package logkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит logkit.Config / ServiceConfig — caller обязан
// зарегистрировать их сам (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(logkit.NewConfig, logkit.NewServiceConfig)`.
//
// service.name / service.version / deployment.environment.name НЕ дублируются
// как record attrs — они уже в OTel resource attrs (telemetry.Init).
// registerServiceAttrs остаётся как helper для test-сценариев, но не зовётся
// модулем — иначе attrs шли бы дважды в каждый OTLP log record.
var Module = fx.Module(
	"logkit",
	fx.Provide(
		New,
		// *slog.Logger для consumer'ов которые не используют logkit.Logger
		// interface (timerkit и пр.). Тот же handler-chain что и Logger —
		// единое место конфигурации.
		NewSlog,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func NewServiceConfig() (ServiceConfig, error) {
	var c ServiceConfig
	return c, envconfig.Process("", &c)
}
