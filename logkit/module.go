package logkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("logkit",
	fx.Provide(
		newConfig,
		newServiceConfig,
		New,
		// *slog.Logger для consumer'ов которые не используют logkit.Logger
		// interface (timerkit и пр.). Тот же handler-chain (trace IDs,
		// JSON), что и Logger — единое место конфигурации.
		NewSlog,
	),
	fx.Invoke(registerServiceAttrs),
)

func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newServiceConfig() (ServiceConfig, error) {
	var c ServiceConfig
	return c, envconfig.Process("", &c)
}
