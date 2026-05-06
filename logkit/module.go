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
