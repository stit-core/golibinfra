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

	"git.stit.tech/stit-infra/golibinfra/pgkit"
)

type PostgresFixture struct {
	DSN       string
	Container *tcpostgres.PostgresContainer
}

func StartPostgres(t *testing.T, migrationsPath string) *PostgresFixture {
	t.Helper()
	ctx := context.Background()

	c, err := tcpostgres.Run(
		ctx, "postgres:17-alpine",
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

	if migrationsPath != "" {
		if err := pgkit.MigrateUp(pgkit.MigrateConfig{DSN: dsn, Path: migrationsPath}); err != nil {
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
