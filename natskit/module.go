package natskit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(natskit.NewConfig)`.
var Module = fx.Module("natskit",
	fx.Provide(
		newConn,
		fx.Annotate(NewBus, fx.As(new(Bus))),
		NewStreamBus,
		Probe,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
