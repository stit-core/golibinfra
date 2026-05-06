package natskit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("natskit",
	fx.Provide(
		newConfig,
		newConn,
		fx.Annotate(NewBus, fx.As(new(Bus))),
		Probe,
	),
)


func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
