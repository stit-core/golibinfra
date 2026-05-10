package logkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит logkit.Config / ServiceConfig — caller обязан
// зарегистрировать их сам (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(logkit.NewConfig, logkit.NewServiceConfig)`.
var Module = fx.Module("logkit",
	fx.Provide(
		New,
		// *slog.Logger для consumer'ов которые не используют logkit.Logger
		// interface (timerkit и пр.). Тот же handler-chain (trace IDs,
		// JSON), что и Logger — единое место конфигурации.
		NewSlog,
	),
	fx.Invoke(registerServiceAttrs),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func NewServiceConfig() (ServiceConfig, error) {
	var c ServiceConfig
	return c, envconfig.Process("", &c)
}
