package pgkit

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	DSN             string        `envconfig:"PG_DSN" required:"true"`
	MaxConns        int32         `envconfig:"PG_MAX_CONNS" default:"10"`
	MinConns        int32         `envconfig:"PG_MIN_CONNS" default:"2"`
	MaxConnLifetime time.Duration `envconfig:"PG_MAX_CONN_LIFETIME" default:"30m"`
	MaxConnIdleTime time.Duration `envconfig:"PG_MAX_CONN_IDLE_TIME" default:"5m"`
	HealthCheck     time.Duration `envconfig:"PG_HEALTH_CHECK" default:"30s"`
	OTelTrace       bool          `envconfig:"PG_OTEL_TRACE" default:"true"`

	// Session-level timeouts. Zero value disables the corresponding setting.
	StatementTimeout       time.Duration `envconfig:"PG_STATEMENT_TIMEOUT" default:"30s"`
	IdleInTxSessionTimeout time.Duration `envconfig:"PG_IDLE_IN_TX_SESSION_TIMEOUT" default:"60s"`
	LockTimeout            time.Duration `envconfig:"PG_LOCK_TIMEOUT" default:"5s"`
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
	if cfg.OTelTrace {
		pcfg.ConnConfig.Tracer = otelpgx.NewTracer()
	}

	prevAfterConnect := pcfg.AfterConnect
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if prevAfterConnect != nil {
			if err := prevAfterConnect(ctx, conn); err != nil {
				return err
			}
		}
		return applySessionTimeouts(ctx, conn, cfg)
	}

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

func applySessionTimeouts(ctx context.Context, conn *pgx.Conn, cfg PoolConfig) error {
	settings := []struct {
		name string
		dur  time.Duration
	}{
		{"statement_timeout", cfg.StatementTimeout},
		{"idle_in_transaction_session_timeout", cfg.IdleInTxSessionTimeout},
		{"lock_timeout", cfg.LockTimeout},
	}
	for _, s := range settings {
		ms := s.dur.Milliseconds()
		if ms < 0 {
			ms = 0
		}
		// SET <name> = '<ms>ms'; ms=0 disables the setting.
		stmt := fmt.Sprintf("SET %s = '%dms'", s.name, ms)
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("pgkit: set %s: %w", s.name, err)
		}
	}
	return nil
}
