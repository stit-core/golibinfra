package flagkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("flagkit",
	fx.Provide(
		newConfig,
		fx.Annotate(NewEnvStore, fx.As(new(Store))),
	),
)

func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
