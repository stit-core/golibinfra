package pgstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// ErrSchema — схема хранилища не на последней миграции кита: миграции не
// применены, отстают или остались dirty.
var ErrSchema = errors.New("authnkit/session/pgstore: schema not migrated")

const (
	// versionTable — Migrations.VersionTable(): литерал, а не конкатенация в SQL.
	versionTable = "schema_migrations_authnkit_session"
	versionSQL   = `SELECT version, dirty FROM schema_migrations_authnkit_session`
	schemaHint   = "apply pgstore.Migrations (pgkit.MigrateModule or the migrate command)"
)

// CheckSchema сверяет таблицу версий источника Migrations с последней миграцией
// кита: без неё запись сессии упала бы на первом входе, а не на старте. Нарушение —
// ErrSchema с подсказкой. Module зовёт её в OnStart после pgkit.MigrateModule.
func (s *Store) CheckSchema(ctx context.Context) error {
	want, err := latestVersion(migrationsFS)
	if err != nil {
		return err
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, versionTable).Scan(&exists); err != nil {
		return fmt.Errorf("authnkit/session/pgstore: schema check: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: %s is missing — %s", ErrSchema, versionTable, schemaHint)
	}
	var (
		version int64
		dirty   bool
	)
	switch err := s.db.QueryRow(ctx, versionSQL).Scan(&version, &dirty); {
	case pgkit.IsNoRows(err):
		return fmt.Errorf("%w: no migration applied — %s", ErrSchema, schemaHint)
	case err != nil:
		return fmt.Errorf("authnkit/session/pgstore: schema check: %w", err)
	case dirty:
		return fmt.Errorf("%w: version %d is dirty — repair the failed migration manually", ErrSchema, version)
	case version < want:
		return fmt.Errorf("%w: version %d, the kit needs %d — %s", ErrSchema, version, want, schemaHint)
	}
	return nil
}

// latestVersion — номер последней up-миграции источника (префикс имени до «_»).
func latestVersion(fsys fs.FS) (int64, error) {
	names, err := fs.Glob(fsys, "migrations/*.up.sql")
	if err != nil {
		return 0, fmt.Errorf("authnkit/session/pgstore: migrations: %w", err)
	}
	var latest int64
	for _, name := range names {
		prefix, _, _ := strings.Cut(path.Base(name), "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("authnkit/session/pgstore: migration %s: %w", name, err)
		}
		latest = max(latest, v)
	}
	return latest, nil
}
