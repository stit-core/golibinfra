package cachekit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (см. asymmetry note в pgkit/module.go) —
// caller обязан зарегистрировать сам. Дешёвый mode:
// `fx.Provide(cachekit.NewConfig)`.
var Module = fx.Module("cachekit",
	fx.Provide(
		fx.Annotate(newRedisCache, fx.As(new(Cache))),
		Probe,
	),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newRedisCache(cfg Config) (Cache, error) {
	return NewRedisCache(cfg)
}
