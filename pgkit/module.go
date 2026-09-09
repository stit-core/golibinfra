package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module — граф пула primary: *pgxpool.Pool, *DB, Transactor и *ReadWriteDB.
// Config'и provide'ит caller (App-структура или fx.Provide(pgkit.NewPoolConfig)).
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

// NewPoolConfig читает PoolConfig из ENV и валидирует его.
func NewPoolConfig() (PoolConfig, error) {
	var c PoolConfig
	if err := envconfig.Process("", &c); err != nil {
		return c, fmt.Errorf("pgkit: pool config: %w", err)
	}
	return c, c.Validate()
}

// NewReplicaConfig читает ReplicaConfig из ENV и валидирует его.
func NewReplicaConfig() (ReplicaConfig, error) {
	var c ReplicaConfig
	if err := envconfig.Process("", &c); err != nil {
		return c, fmt.Errorf("pgkit: replica config: %w", err)
	}
	return c, c.Validate()
}

func newPoolWithLifecycle(lc fx.Lifecycle, cfg PoolConfig) (*pgxpool.Pool, error) {
	return openPool(lc, cfg, RolePrimary)
}

// replicaParams: ReplicaConfig опционален — сервис без реплики не обязан его
// провайдить, zero value означает «реплики нет».
type replicaParams struct {
	fx.In

	LC      fx.Lifecycle
	Master  *DB
	Pool    PoolConfig
	Replica ReplicaConfig `optional:"true"`
}

func newReadWriteDB(p replicaParams) (*ReadWriteDB, error) {
	if err := p.Replica.Validate(); err != nil {
		return nil, err
	}
	if p.Replica.ReplicaDSN == "" {
		return NewReadWriteDB(p.Master, p.Master), nil
	}

	repPoolCfg, err := p.Replica.poolConfig(p.Pool)
	if err != nil {
		return nil, err
	}

	pool, err := openPool(p.LC, repPoolCfg, RoleReplica)
	if err != nil {
		return nil, fmt.Errorf("pgkit: replica: %w", err)
	}
	return NewReadWriteDB(p.Master, NewDB(pool)), nil
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
