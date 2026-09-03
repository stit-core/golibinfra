package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config'и: их регистрирует caller (App-структура или
// `fx.Provide(pgkit.NewPoolConfig, pgkit.NewReplicaConfig)`), как у всех китов.
var Module = fx.Module("pgkit",
	fx.Provide(
		newPoolWithLifecycle,
		NewDB,
		fx.Annotate(NewPoolTransactor, fx.As(new(Transactor))),
		newReadWriteDB,
	),
	// Пулы поднимаются на старте, а не при первом запросе: недоступная БД валит запуск.
	fx.Invoke(func(*ReadWriteDB) {}),
)

func NewPoolConfig() (PoolConfig, error) {
	var c PoolConfig
	return c, envconfig.Process("", &c)
}

func NewReplicaConfig() (ReplicaConfig, error) {
	var c ReplicaConfig
	return c, envconfig.Process("", &c)
}

func newPoolWithLifecycle(lc fx.Lifecycle, cfg PoolConfig) (*pgxpool.Pool, error) {
	return openPool(lc, cfg, RolePrimary)
}

func newReadWriteDB(lc fx.Lifecycle, master *DB, repCfg ReplicaConfig, poolCfg PoolConfig) (*ReadWriteDB, error) {
	if repCfg.ReplicaDSN == "" {
		return NewReadWriteDB(master, master), nil
	}

	repPoolCfg := poolCfg
	repPoolCfg.DSN = repCfg.ReplicaDSN
	repPoolCfg.MaxConns = repCfg.MaxConns

	pool, err := openPool(lc, repPoolCfg, RoleReplica)
	if err != nil {
		return nil, fmt.Errorf("pgkit: replica: %w", err)
	}
	return NewReadWriteDB(master, NewDB(pool)), nil
}

func openPool(lc fx.Lifecycle, cfg PoolConfig, role string) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithCancel(context.Background())

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	reg := InstrumentPool(pool, role)

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			err := reg.Unregister()
			pool.Close()
			cancel()
			return err
		},
	})
	return pool, nil
}
