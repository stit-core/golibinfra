package pgkit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	DSN             string        `envconfig:"PG_DSN" required:"true"`
	MaxConns        int32         `envconfig:"PG_MAX_CONNS" default:"10"`
	MinConns        int32         `envconfig:"PG_MIN_CONNS" default:"2"`
	MaxConnLifetime time.Duration `envconfig:"PG_MAX_CONN_LIFETIME" default:"30m"`
	MaxConnIdleTime time.Duration `envconfig:"PG_MAX_CONN_IDLE_TIME" default:"5m"`
	HealthCheck     time.Duration `envconfig:"PG_HEALTH_CHECK" default:"30s"`
}

func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgkit: parse dsn: %w", err)
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MinConns = cfg.MinConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	pcfg.HealthCheckPeriod = cfg.HealthCheck

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pgkit: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgkit: ping: %w", err)
	}
	return pool, nil
}
