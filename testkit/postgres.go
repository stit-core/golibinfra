// Package testkit — testcontainers-обёртки для интеграционных тестов:
// StartPostgres/StartNATS/StartRedis поднимают контейнер на свободный
// порт и регистрируют t.Cleanup для остановки. Подпакеты
// (audittest, authntest, bruteforcetest, eventbus, handlertest, txkit)
// дают per-kit'овые helper'ы.
package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

const PostgresImage = "postgres:17-alpine"

type PostgresFixture struct {
	DSN       string
	Container *tcpostgres.PostgresContainer
}

type PostgresOption func(*postgresOptions)

type postgresOptions struct {
	sources []pgkit.Source
}

// WithMigrations применяет источники (embed.FS китов, каталоги приложения)
// сразу после старта контейнера — через pgkit.MigrateUpAll, как MigrateModule.
func WithMigrations(sources ...pgkit.Source) PostgresOption {
	return func(o *postgresOptions) { o.sources = append(o.sources, sources...) }
}

// WithMigrationsDir — каталог миграций приложения (источник pgkit.AppSourceName).
func WithMigrationsDir(path string) PostgresOption {
	return WithMigrations(pgkit.DirSource(pgkit.AppSourceName, path))
}

func StartPostgres(t *testing.T, opts ...PostgresOption) *PostgresFixture {
	t.Helper()
	var o postgresOptions
	for _, opt := range opts {
		opt(&o)
	}
	ctx := context.Background()

	c, err := tcpostgres.Run(
		ctx, PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("testkit: start postgres: %v", err)
	}

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testkit: dsn: %v", err)
	}

	t.Cleanup(func() {
		_ = c.Terminate(context.Background())
	})

	if len(o.sources) > 0 {
		migCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := pgkit.MigrateUpAll(migCtx, dsn, o.sources...); err != nil {
			t.Fatalf("testkit: migrate up: %v", err)
		}
	}

	return &PostgresFixture{DSN: dsn, Container: c}
}

func (f *PostgresFixture) DB(t *testing.T) *pgkit.DB {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), f.DSN)
	if err != nil {
		t.Fatalf("testkit: pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pgkit.NewDB(pool)
}
