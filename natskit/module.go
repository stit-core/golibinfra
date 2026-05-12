package natskit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go) и не provide'ит
// Probe. Probe имеет тип `func(context.Context) error`, который коллизит с
// cachekit.Probe (и любым другим health-probe). Caller сам регистрирует с нужной
// аннотацией, обычно `group:"health"`:
//
//	fx.Provide(fx.Annotate(natskit.Probe, fx.ResultTags(`group:"health"`)))
var Module = fx.Module(
	"natskit",
	fx.Provide(
		newConn,
		fx.Annotate(NewBus, fx.As(new(Bus))),
		NewStreamBus,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
