package pgkit

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migrateAdvisoryLockKey = "golibinfra-migrate"

func advisoryLockKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(migrateAdvisoryLockKey))
	return int64(h.Sum64())
}

func runMigrateUpLocked(ctx context.Context, dsn, path string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("pgkit: migrate lock: parse dsn: %w", err)
	}
	cfg.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pgkit: migrate lock: connect: %w", err)
	}
	defer pool.Close()

	key := advisoryLockKey()

	if _, err := pool.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("pgkit: migrate lock: acquire: %w", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	}()

	return MigrateUp(MigrateConfig{DSN: dsn, Path: path})
}
