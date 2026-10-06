package pgkit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"
)

// ErrSchema — схема источника не на его последней миграции: таблицы версий нет, ни одна
// миграция не применена, версия отстаёт или dirty.
var ErrSchema = errors.New("pgkit: schema not migrated")

const schemaHint = "apply it with pgkit.MigrateModule or the migrate command"

// CheckSchema сверяет таблицу версий источника с его последней миграцией: хранилище кита без
// своих миграций падает на старте, а не на первой записи. Нарушение — ErrSchema с именем
// источника и подсказкой, как его применить.
func CheckSchema(ctx context.Context, db DBTX, src Source) error {
	if err := src.validate(); err != nil {
		return err
	}
	want, err := src.latestVersion()
	if err != nil {
		return err
	}
	table := src.VersionTable()
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		return fmt.Errorf("pgkit: schema check of source %q: %w", src.Name, err)
	}
	if !exists {
		return fmt.Errorf("%w: source %q: %s is missing — %s", ErrSchema, src.Name, table, schemaHint)
	}
	var (
		version uint64
		dirty   bool
	)
	switch err := db.QueryRow(ctx, `SELECT version, dirty FROM `+pgx.Identifier{table}.Sanitize()).Scan(&version, &dirty); {
	case IsNoRows(err):
		return fmt.Errorf("%w: source %q: no migration applied — %s", ErrSchema, src.Name, schemaHint)
	case err != nil:
		return fmt.Errorf("pgkit: schema check of source %q: %w", src.Name, err)
	case dirty:
		return fmt.Errorf("%w: source %q: version %d is dirty — repair the failed migration manually", ErrSchema, src.Name, version)
	case version < uint64(want):
		return fmt.Errorf("%w: source %q: version %d, the source needs %d — %s", ErrSchema, src.Name, version, want, schemaHint)
	}
	return nil
}

// CheckSchemaOnStart вешает CheckSchema источника на OnStart после миграций (optional
// Migrated): так Module хранилища кита отказывает на старте без своих миграций. Нужен *DB.
func CheckSchemaOnStart(src Source) fx.Option {
	return fx.Invoke(func(p schemaCheckParams) {
		p.LC.Append(fx.Hook{OnStart: func(ctx context.Context) error { return CheckSchema(ctx, p.DB, src) }})
	})
}

// schemaCheckParams: optional Migrated ставит хук в очередь после миграций, а сборка без
// MigrateModule (миграции накатывает отдельная команда) остаётся рабочей.
type schemaCheckParams struct {
	fx.In

	LC       fx.Lifecycle
	DB       *DB
	Migrated Migrated `optional:"true"`
}

// latestVersion — номер последней миграции источника по правилу имён golang-migrate.
func (s Source) latestVersion() (uint, error) {
	drv, err := iofs.New(s.FS, s.Dir)
	if err != nil {
		return 0, fmt.Errorf("pgkit: source %q: %w", s.Name, err)
	}
	defer func() { _ = drv.Close() }()
	v, err := drv.First()
	if err != nil {
		return 0, fmt.Errorf("pgkit: source %q: no migrations in %q", s.Name, s.Dir)
	}
	for {
		next, err := drv.Next(v)
		if errors.Is(err, fs.ErrNotExist) {
			return v, nil
		}
		if err != nil {
			return 0, fmt.Errorf("pgkit: source %q: %w", s.Name, err)
		}
		v = next
	}
}
