package pgkit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/internal/stopbudget"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module — граф пула primary: *pgxpool.Pool, *DB, Transactor и *ReadWriteDB.
// Config'и provide'ит caller (App-структура или fx.Provide(pgkit.NewPoolConfig)), логгер —
// logkit.Module.
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
	if err := envconf.Load(&c); err != nil {
		return PoolConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// NewReplicaConfig читает ReplicaConfig из ENV и валидирует его.
func NewReplicaConfig() (ReplicaConfig, error) {
	var c ReplicaConfig
	if err := envconf.Load(&c); err != nil {
		return ReplicaConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

func newPoolWithLifecycle(lc fx.Lifecycle, cfg PoolConfig, log logkit.Logger) (*pgxpool.Pool, error) {
	return openPool(lc, cfg, RolePrimary, log)
}

// replicaParams: ReplicaConfig опционален — сервис без реплики не обязан его
// провайдить, zero value означает «реплики нет».
type replicaParams struct {
	fx.In

	LC      fx.Lifecycle
	Master  *DB
	Pool    PoolConfig
	Replica ReplicaConfig `optional:"true"`
	Log     logkit.Logger
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

	pool, err := openPool(p.LC, repPoolCfg, RoleReplica, p.Log)
	if err != nil {
		return nil, fmt.Errorf("pgkit: replica: %w", err)
	}
	return NewReadWriteDB(p.Master, NewDB(pool)), nil
}

func openPool(lc fx.Lifecycle, cfg PoolConfig, role string, log logkit.Logger) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithCancel(context.Background())

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	// Callback регистрирует OnStart: пул графа, не дошедшего до старта, не оставляет его в MeterProvider.
	var reg metric.Registration
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			reg = InstrumentPool(pool, role)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			defer cancel()
			err := reg.Unregister()
			// Close ждёт возврата каждого занятого соединения: забытая транзакция не должна съесть
			// бюджет остановки хуков после пула (телеметрия), поэтому им остаётся треть.
			closed := make(chan struct{})
			go func() {
				pool.Close()
				close(closed)
			}()
			wait, stop := stopbudget.Share(ctx)
			defer stop()
			select {
			case <-closed:
			case <-wait.Done():
				log.Warn(ctx, "pgkit: pool close is still waiting for acquired connections",
					"role", role, "acquired_conns", pool.Stat().AcquiredConns())
			}
			return err
		},
	})
	return pool, nil
}
