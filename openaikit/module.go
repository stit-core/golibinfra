package openaikit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (симметрия с pgkit/anthropickit). Дешёвый путь:
// fx.Provide(openaikit.NewConfig). Опции (WithPriceTable, WithRequestOptions для
// Azure и т.п.) — через свой конструктор поверх New, если нужны.
var Module = fx.Module(
	"openaikit",
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
