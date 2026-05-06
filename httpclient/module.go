package httpclient

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("httpclient",
	fx.Provide(
		newConfig,
		New,
	),
)

func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
