package pgkit

import (
	"context"
	"fmt"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

type MigrateRunnerConfig struct {
	Path string `envconfig:"PG_MIGRATIONS_PATH" default:"/migrations"`
}

func newMigrateRunnerConfig() (MigrateRunnerConfig, error) {
	var c MigrateRunnerConfig
	return c, envconfig.Process("", &c)
}

var MigrateModule = fx.Module("pgkit-migrate",
	fx.Provide(newMigrateRunnerConfig),
	fx.Invoke(runMigrationsOnStart),
)

func runMigrationsOnStart(lc fx.Lifecycle, pool PoolConfig, mig MigrateRunnerConfig) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := runMigrateUpLocked(ctx, pool.DSN, mig.Path); err != nil {
				return fmt.Errorf("pgkit: migrate on start: %w", err)
			}
			return nil
		},
	})
}
