package cachekit

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config (caller регистрирует его сам) и не кладёт Probe в
// граф: пробу регистрирует потребитель через healthkit.Provide(cachekit.Probe).
var Module = fx.Module(
	"cachekit",
	fx.Provide(newCacheWithLifecycle),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newCacheWithLifecycle(lc fx.Lifecycle, cfg Config) (Cache, error) {
	c, err := NewRedisCache(cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if !cfg.PingOnStart {
				return nil
			}
			return c.Ping(ctx)
		},
		OnStop: func(context.Context) error { return c.Close() },
	})
	return c, nil
}
