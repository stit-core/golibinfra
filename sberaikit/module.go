package sberaikit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go). Дешёвый mode:
// fx.Provide(sberaikit.NewConfig). Опции (WithPriceTable и т.п.) — через свой
// конструктор поверх New, если нужны. Требует clockkit.Clock в графе (для
// OAuth-TTL); logkit.Logger — опционально (Nop по nil).
var Module = fx.Module(
	"sberaikit",
	fx.Provide(
		fx.Annotate(newClient, fx.As(new(API))),
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newClient(cfg Config, log logkit.Logger, clock clockkit.Clock) (*Client, error) {
	return New(cfg, log, clock)
}
