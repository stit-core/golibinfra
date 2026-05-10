package httpclient

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go).
// Дешёвый mode: `fx.Provide(httpclient.NewConfig)`.
var Module = fx.Module("httpclient",
	fx.Provide(
		New,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
