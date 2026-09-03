package pgkit

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"
)

const migrateAdvisoryLockKey = "golibinfra-migrate"

func advisoryLockKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(migrateAdvisoryLockKey))
	return int64(h.Sum64()) //nolint:gosec // intentional bit-reinterpret of FNV-1a hash into pg advisory-lock int64 key
}

// MigrateUpAll применяет источники по порядку под одним advisory-lock'ом:
// из одновременно стартующих инстансов мигрирует ровно один.
func MigrateUpAll(ctx context.Context, dsn string, sources ...Source) error {
	if len(sources) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		if err := src.validate(); err != nil {
			return err
		}
		if _, dup := seen[src.Name]; dup {
			return fmt.Errorf("pgkit: migrate: duplicate source name %q", src.Name)
		}
		seen[src.Name] = struct{}{}
	}

	connCfg, err := migrateConnConfig(ctx, dsn)
	if err != nil {
		return err
	}
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		return fmt.Errorf("pgkit: migrate lock: connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	key := advisoryLockKey()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("pgkit: migrate lock: acquire: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key)
	}()

	for _, src := range sources {
		if err := MigrateUp(ctx, dsn, src); err != nil {
			return err
		}
	}
	return nil
}
