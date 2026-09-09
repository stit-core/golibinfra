package pgkit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig — параметры пула primary Postgres; читается из ENV через
// NewPoolConfig. Нулевые размеры и таймауты пула означают «дефолт pgxpool».
type PoolConfig struct {
	DSN             string        `envconfig:"PG_DSN" required:"true"`
	MaxConns        int32         `envconfig:"PG_MAX_CONNS" default:"10"`
	MinConns        int32         `envconfig:"PG_MIN_CONNS" default:"2"`
	MaxConnLifetime time.Duration `envconfig:"PG_MAX_CONN_LIFETIME" default:"30m"`
	MaxConnIdleTime time.Duration `envconfig:"PG_MAX_CONN_IDLE_TIME" default:"5m"`
	HealthCheck     time.Duration `envconfig:"PG_HEALTH_CHECK" default:"30s"`
	ConnectTimeout  time.Duration `envconfig:"PG_CONNECT_TIMEOUT" default:"10s"`
	OTelTrace       bool          `envconfig:"PG_OTEL_TRACE" default:"true"`

	StatementTimeout       time.Duration `envconfig:"PG_STATEMENT_TIMEOUT" default:"30s"`
	IdleInTxSessionTimeout time.Duration `envconfig:"PG_IDLE_IN_TX_SESSION_TIMEOUT" default:"60s"`
	LockTimeout            time.Duration `envconfig:"PG_LOCK_TIMEOUT" default:"5s"`
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig.
func (c PoolConfig) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("%w: PG_DSN is empty", ErrConfig)
	}
	if c.MaxConns < 0 {
		return fmt.Errorf("%w: PG_MAX_CONNS must be >= 0, got %d", ErrConfig, c.MaxConns)
	}
	if c.MinConns < 0 {
		return fmt.Errorf("%w: PG_MIN_CONNS must be >= 0, got %d", ErrConfig, c.MinConns)
	}
	if c.MaxConns > 0 && c.MinConns > c.MaxConns {
		return fmt.Errorf("%w: PG_MIN_CONNS (%d) must be <= PG_MAX_CONNS (%d)", ErrConfig, c.MinConns, c.MaxConns)
	}
	durations := []struct {
		env string
		d   time.Duration
	}{
		{"PG_MAX_CONN_LIFETIME", c.MaxConnLifetime},
		{"PG_MAX_CONN_IDLE_TIME", c.MaxConnIdleTime},
		{"PG_HEALTH_CHECK", c.HealthCheck},
		{"PG_CONNECT_TIMEOUT", c.ConnectTimeout},
		{"PG_STATEMENT_TIMEOUT", c.StatementTimeout},
		{"PG_IDLE_IN_TX_SESSION_TIMEOUT", c.IdleInTxSessionTimeout},
		{"PG_LOCK_TIMEOUT", c.LockTimeout},
	}
	for _, d := range durations {
		if d.d < 0 {
			return fmt.Errorf("%w: %s must be >= 0, got %s", ErrConfig, d.env, d.d)
		}
	}
	return nil
}

// NewPool открывает пул и проверяет его Ping'ом. ctx живёт с пулом (прогрев
// MinConns идёт в фоне на нём); ConnectTimeout ограничивает каждый коннект и Ping.
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgkit: parse dsn: %w", err)
	}
	// Ноль означает «оставить дефолт pgxpool»: перенесённый в пул, он ломает его
	// (HealthCheckPeriod=0 — panic в фоновой горутине, MaxConns=0 — отказ puddle).
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheck > 0 {
		pcfg.HealthCheckPeriod = cfg.HealthCheck
	}
	if cfg.ConnectTimeout > 0 {
		pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	if cfg.OTelTrace {
		pcfg.ConnConfig.Tracer = otelpgx.NewTracer()
	}
	applySessionTimeouts(pcfg, cfg)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pgkit: connect: %w", err)
	}

	pingCtx, cancel := withOptionalTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgkit: ping: %w", err)
	}
	return pool, nil
}

func withOptionalTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// applySessionTimeouts кладёт таймауты в StartupMessage: SET'ы в AfterConnect
// стоили бы трёх round-trip'ов на каждое новое физическое соединение.
func applySessionTimeouts(pcfg *pgxpool.Config, cfg PoolConfig) {
	if pcfg.ConnConfig.RuntimeParams == nil {
		pcfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	for name, d := range map[string]time.Duration{
		"statement_timeout":                   cfg.StatementTimeout,
		"idle_in_transaction_session_timeout": cfg.IdleInTxSessionTimeout,
		"lock_timeout":                        cfg.LockTimeout,
	} {
		ms := max(d.Milliseconds(), 0)
		pcfg.ConnConfig.RuntimeParams[name] = strconv.FormatInt(ms, 10)
	}
}
