package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Asymmetry note: Module НЕ provide'ит pgkit.PoolConfig — caller
// обязан зарегистрировать его сам (типично через App-структуру в
// app-config модуле). Аналогично telemetry.Module — telemetry.Config
// ждёт извне. Только httpkit.Module provide'ит свой Config через
// httpkit.newConfig. Если добавить newPoolConfig сюда, у app-side
// провайдера получится fx-конфликт "already provided".
var Module = fx.Module("pgkit",
	fx.Provide(
		newPoolWithLifecycle,
		NewDB,
		fx.Annotate(NewPoolTransactor, fx.As(new(Transactor))),
		newReplicaConfig,
		newReadWriteDB,
	),
)

func newReplicaConfig() (ReplicaConfig, error) {
	var c ReplicaConfig
	return c, envconfig.Process("", &c)
}

func newReadWriteDB(lc fx.Lifecycle, master *DB, repCfg ReplicaConfig, poolCfg PoolConfig) (*ReadWriteDB, error) {
	if repCfg.ReplicaDSN == "" {
		return NewReadWriteDB(master, master), nil
	}

	repPoolCfg := PoolConfig{
		DSN:             repCfg.ReplicaDSN,
		MaxConns:        repCfg.MaxConns,
		MinConns:        poolCfg.MinConns,
		MaxConnLifetime: poolCfg.MaxConnLifetime,
		MaxConnIdleTime: poolCfg.MaxConnIdleTime,
		HealthCheck:     poolCfg.HealthCheck,
		OTelTrace:       poolCfg.OTelTrace,
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool, err := NewPool(ctx, repPoolCfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("pgkit: replica pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			pool.Close()
			cancel()
			return nil
		},
	})

	replica := NewDB(pool)
	return NewReadWriteDB(master, replica), nil
}

func newPoolWithLifecycle(lc fx.Lifecycle, cfg PoolConfig) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithCancel(context.Background())

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			pool.Close()
			cancel()
			return nil
		},
	})

	return pool, nil
}
