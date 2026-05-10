package flagkit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(flagkit.NewConfig)`.
var Module = fx.Module("flagkit",
	fx.Provide(
		fx.Annotate(NewEnvStore, fx.As(new(Store))),
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
