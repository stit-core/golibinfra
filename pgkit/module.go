package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Asymmetry note (golibinfra ≥ v0.17): ни один kit не provide'ит свой
// Config внутренне. Caller обязан регистрировать Config'и сам (типично
// через App-структуру в app-config модуле). Дешёвый mode без
// App-pattern — `fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig)`.
// Это однородно с httpkit/telemetry/authnkit/cachekit/flagkit/natskit/
// logkit/httpclient — у каждого Config-фабрика exported, но не в Module.
var Module = fx.Module("pgkit",
	fx.Provide(
		newPoolWithLifecycle,
		NewDB,
		fx.Annotate(NewPoolTransactor, fx.As(new(Transactor))),
		newReadWriteDB,
	),
)

func NewPoolConfig() (PoolConfig, error) {
	var c PoolConfig
	return c, envconfig.Process("", &c)
}

func NewReplicaConfig() (ReplicaConfig, error) {
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
