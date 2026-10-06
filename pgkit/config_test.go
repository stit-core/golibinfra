package pgkit_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

func TestPoolConfig_Validate(t *testing.T) {
	t.Parallel()
	valid := pgkit.PoolConfig{DSN: "postgres://u@h/d", MaxConns: 10, MinConns: 2, HealthCheck: 30 * time.Second}

	tests := []struct {
		name    string
		mutate  func(*pgkit.PoolConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*pgkit.PoolConfig) {}},
		{name: "zero sizes fall back to pgx defaults", mutate: func(c *pgkit.PoolConfig) {
			c.MaxConns, c.MinConns, c.HealthCheck = 0, 0, 0
		}},
		{name: "empty dsn", mutate: func(c *pgkit.PoolConfig) { c.DSN = "" }, wantErr: true},
		{name: "negative max conns", mutate: func(c *pgkit.PoolConfig) { c.MaxConns = -1 }, wantErr: true},
		{name: "negative min conns", mutate: func(c *pgkit.PoolConfig) { c.MinConns = -1 }, wantErr: true},
		{name: "min above max", mutate: func(c *pgkit.PoolConfig) { c.MinConns = 11 }, wantErr: true},
		{name: "negative lifetime", mutate: func(c *pgkit.PoolConfig) { c.MaxConnLifetime = -time.Second }, wantErr: true},
		{name: "negative idle time", mutate: func(c *pgkit.PoolConfig) { c.MaxConnIdleTime = -time.Second }, wantErr: true},
		{name: "negative health check", mutate: func(c *pgkit.PoolConfig) { c.HealthCheck = -time.Second }, wantErr: true},
		{name: "negative connect timeout", mutate: func(c *pgkit.PoolConfig) { c.ConnectTimeout = -time.Second }, wantErr: true},
		{name: "negative statement timeout", mutate: func(c *pgkit.PoolConfig) { c.StatementTimeout = -time.Second }, wantErr: true},
		{name: "negative idle in tx", mutate: func(c *pgkit.PoolConfig) { c.IdleInTxSessionTimeout = -time.Second }, wantErr: true},
		{name: "negative lock timeout", mutate: func(c *pgkit.PoolConfig) { c.LockTimeout = -time.Second }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tt.mutate(&c)
			err := c.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, pgkit.ErrConfig) {
				t.Fatalf("Validate() = %v, want errors.Is ErrConfig", err)
			}
		})
	}
}

func TestReplicaConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     pgkit.ReplicaConfig
		wantErr bool
	}{
		{name: "zero value means no replica", cfg: pgkit.ReplicaConfig{}},
		{name: "dsn and conns", cfg: pgkit.ReplicaConfig{ReplicaDSN: "postgres://u@h/d", MaxConns: 10}},
		{name: "negative conns", cfg: pgkit.ReplicaConfig{MaxConns: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, pgkit.ErrConfig) {
				t.Fatalf("Validate() = %v, want errors.Is ErrConfig", err)
			}
		})
	}
}

func TestMigrateRunnerConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     pgkit.MigrateRunnerConfig
		wantErr bool
	}{
		{name: "path and timeouts", cfg: pgkit.MigrateRunnerConfig{Path: "/migrations", Timeout: 2 * time.Minute, LockTimeout: 5 * time.Second}},
		{name: "empty path skips app source", cfg: pgkit.MigrateRunnerConfig{Path: "", Timeout: time.Minute, LockTimeout: time.Second}},
		{name: "zero timeout", cfg: pgkit.MigrateRunnerConfig{Path: "/migrations", LockTimeout: time.Second}, wantErr: true},
		{name: "negative timeout", cfg: pgkit.MigrateRunnerConfig{Path: "/m", Timeout: -time.Second, LockTimeout: time.Second}, wantErr: true},
		{name: "zero lock timeout", cfg: pgkit.MigrateRunnerConfig{Path: "/m", Timeout: time.Minute}, wantErr: true},
		{name: "negative lock timeout", cfg: pgkit.MigrateRunnerConfig{Path: "/m", Timeout: time.Minute, LockTimeout: -time.Second}, wantErr: true},
		{name: "padded path", cfg: pgkit.MigrateRunnerConfig{Path: " /migrations ", Timeout: time.Minute, LockTimeout: time.Second}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, pgkit.ErrConfig) {
				t.Fatalf("Validate() = %v, want errors.Is ErrConfig", err)
			}
		})
	}
}

// Дефолты envconfig — часть public API кита.
func TestNewConfigs_Defaults(t *testing.T) {
	t.Setenv("PG_DSN", "postgres://u:p@localhost:5432/db")

	pool, err := pgkit.NewPoolConfig()
	if err != nil {
		t.Fatalf("NewPoolConfig: %v", err)
	}
	want := pgkit.PoolConfig{
		DSN:                    "postgres://u:p@localhost:5432/db",
		MaxConns:               10,
		MinConns:               2,
		MaxConnLifetime:        30 * time.Minute,
		MaxConnIdleTime:        5 * time.Minute,
		HealthCheck:            30 * time.Second,
		ConnectTimeout:         10 * time.Second,
		OTelTrace:              true,
		StatementTimeout:       30 * time.Second,
		IdleInTxSessionTimeout: 60 * time.Second,
		LockTimeout:            5 * time.Second,
	}
	if pool != want {
		t.Fatalf("NewPoolConfig() = %+v, want %+v", pool, want)
	}

	rep, err := pgkit.NewReplicaConfig()
	if err != nil {
		t.Fatalf("NewReplicaConfig: %v", err)
	}
	if (rep != pgkit.ReplicaConfig{MaxConns: 10}) {
		t.Fatalf("NewReplicaConfig() = %+v, want {\"\" 10}", rep)
	}

	run, err := pgkit.NewMigrateRunnerConfig()
	if err != nil {
		t.Fatalf("NewMigrateRunnerConfig: %v", err)
	}
	if (run != pgkit.MigrateRunnerConfig{Path: "/migrations", Timeout: 2 * time.Minute, LockTimeout: 5 * time.Second}) {
		t.Fatalf("NewMigrateRunnerConfig() = %+v, want {/migrations 2m 5s}", run)
	}
}

func TestNewPoolConfig_RejectsInvalidEnv(t *testing.T) {
	t.Setenv("PG_DSN", "postgres://u:p@localhost:5432/db")
	t.Setenv("PG_MIN_CONNS", "50")

	if _, err := pgkit.NewPoolConfig(); !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("NewPoolConfig() err = %v, want ErrConfig", err)
	}
}

func TestNewMigrateRunnerConfig_RejectsZeroTimeout(t *testing.T) {
	t.Setenv("PG_MIGRATE_TIMEOUT", "0s")

	if _, err := pgkit.NewMigrateRunnerConfig(); !errors.Is(err, pgkit.ErrConfig) {
		t.Fatalf("NewMigrateRunnerConfig() err = %v, want ErrConfig", err)
	}
}
