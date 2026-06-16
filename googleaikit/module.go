package googleaikit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go). Дешёвый mode:
// fx.Provide(googleaikit.NewConfig). Опции (WithPriceTable и т.п.) — через свой
// конструктор поверх New, если нужны.
var Module = fx.Module(
	"googleaikit",
	fx.Provide(
		fx.Annotate(newClient, fx.As(new(API))),
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newClient(cfg Config, log logkit.Logger) (*Client, error) {
	return New(cfg, log)
}
