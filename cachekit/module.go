package cachekit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go) и не provide'ит
// Probe. Probe имеет тип `func(context.Context) error`, который коллизит с
// natskit.Probe (и любым другим health-probe). Caller сам регистрирует с нужной
// аннотацией, обычно `group:"health"`:
//
//	fx.Provide(fx.Annotate(cachekit.Probe, fx.ResultTags(`group:"health"`)))
var Module = fx.Module(
	"cachekit",
	fx.Provide(
		fx.Annotate(newRedisCache, fx.As(new(Cache))),
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newRedisCache(cfg Config) (Cache, error) {
	return NewRedisCache(cfg)
}
