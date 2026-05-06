package pgkit

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

var Module = fx.Module("pgkit",
	fx.Provide(
		newPoolWithLifecycle,
		NewDB,
		fx.Annotate(NewPoolTransactor, fx.As(new(Transactor))),
	),
)

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
