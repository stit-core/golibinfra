package cachekit

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module не provide'ит Config (caller регистрирует его сам) и не кладёт Probe в
// граф: пробу регистрирует потребитель через healthkit.Provide(cachekit.Probe).
var Module = fx.Module(
	"cachekit",
	fx.Provide(newCacheWithLifecycle),
)

// NewConfig читает REDIS_* из окружения и валидирует их.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

func newCacheWithLifecycle(lc fx.Lifecycle, cfg Config) (Cache, error) {
	c, err := newRedisCache(cfg)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		// Обе проверки требуют живого сервера, поэтому REDIS_PING_ON_START=false
		// снимает и версионную: старт без соединения — осознанный выбор caller'а.
		OnStart: func(ctx context.Context) error {
			if !cfg.PingOnStart {
				return nil
			}
			if err := c.Ping(ctx); err != nil {
				return err
			}
			if cfg.SkipVersionCheck {
				return nil
			}
			return c.checkServerVersion(ctx)
		},
		OnStop: func(context.Context) error { return c.Close() },
	})
	return c, nil
}
