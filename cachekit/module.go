package cachekit

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

var Module = fx.Module("cachekit",
	fx.Provide(
		newConfig,
		fx.Annotate(newRedisCache, fx.As(new(Cache))),
	),
)

func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newRedisCache(cfg Config) (Cache, error) {
	return NewRedisCache(cfg)
}
