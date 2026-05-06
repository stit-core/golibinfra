// Package testkit — хелперы для интеграционных тестов: testcontainers (postgres), httptest.
package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"git.stit.tech/akadmin/golibinfra/pgkit"
)

type PostgresFixture struct {
	DSN       string
	Container *tcpostgres.PostgresContainer
}

// StartPostgres поднимает контейнер postgres:16-alpine, накатывает миграции из migrationsPath
// (если != ""), регистрирует teardown через t.Cleanup. Возвращает DSN с готовой схемой.
func StartPostgres(t *testing.T, migrationsPath string) *PostgresFixture {
	t.Helper()
	ctx := context.Background()

	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
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
