package testkit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// PostgresImage — образ StartPostgres по умолчанию; переопределяется WithImage.
const PostgresImage = "postgres:17-alpine"

const migrateTimeout = 60 * time.Second

// PostgresFixture — живой Postgres теста: DSN, handle контейнера и общий пул.
type PostgresFixture struct {
	DSN       string
	Container *tcpostgres.PostgresContainer

	// owner держит время жизни пула: он переживает подтесты, из которых запрошен.
	owner testing.TB
	mu    sync.Mutex
	pool  *pgxpool.Pool
}

// StartPostgres поднимает Postgres и применяет миграции из опций.
// Под -short пропускает тест, без Docker обрывает (RequireDocker); контейнер
// снимается в t.Cleanup.
func StartPostgres(t testing.TB, opts ...Option) *PostgresFixture {
	t.Helper()
	RequireDocker(t)
	o := newOptions(t, support{helper: "StartPostgres", migrations: true}, PostgresImage, opts)
	ctx := t.Context()

	c, err := tcpostgres.Run(
		ctx, o.image,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithLogger(tclog.TestLogger(t)),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("testkit: start postgres: %v", err)
	}

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testkit: postgres dsn: %v", err)
	}

	if len(o.sources) > 0 {
		migCtx, cancel := context.WithTimeout(ctx, migrateTimeout)
		defer cancel()
		if err := pgkit.MigrateUpAll(migCtx, dsn, o.sources...); err != nil {
			t.Fatalf("testkit: migrate up: %v", err)
		}
	}

	return &PostgresFixture{DSN: dsn, Container: c, owner: t}
}

// PoolConfig — конфиг пула фикстуры: prod-дефолты pgkit поверх DSN контейнера.
func (f *PostgresFixture) PoolConfig() pgkit.PoolConfig {
	return pgkit.PoolConfig{
		DSN:                    f.DSN,
		MaxConns:               5,
		MinConns:               1,
		MaxConnLifetime:        30 * time.Minute,
		MaxConnIdleTime:        5 * time.Minute,
		HealthCheck:            30 * time.Second,
		ConnectTimeout:         10 * time.Second,
		OTelTrace:              true,
		StatementTimeout:       30 * time.Second,
		IdleInTxSessionTimeout: 60 * time.Second,
		LockTimeout:            5 * time.Second,
	}
}

// Pool — общий пул фикстуры через pgkit.NewPool (session-таймауты, otelpgx);
// открывается один раз и закрывается в Cleanup теста, стартовавшего контейнер.
func (f *PostgresFixture) Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pool != nil {
		return f.pool
	}
	pool, err := pgkit.NewPool(f.owner.Context(), f.PoolConfig())
	if err != nil {
		t.Fatalf("testkit: postgres pool: %v", err)
	}
	f.owner.Cleanup(pool.Close)
	f.pool = pool
	return pool
}

// DB — *pgkit.DB поверх Pool.
func (f *PostgresFixture) DB(t testing.TB) *pgkit.DB {
	t.Helper()
	return pgkit.NewDB(f.Pool(t))
}

// Transactor — pgkit.Transactor поверх того же пула, что DB.
func (f *PostgresFixture) Transactor(t testing.TB) pgkit.Transactor {
	t.Helper()
	return pgkit.NewPoolTransactor(f.Pool(t))
}
