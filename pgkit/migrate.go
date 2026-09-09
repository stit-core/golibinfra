package pgkit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Source — источник миграций с собственной таблицей версий
// schema_migrations_<Name>: новая миграция в ките не сдвигает версии приложения.
type Source struct {
	// Name — суффикс таблицы версий, ^[a-z][a-z0-9_]{0,44}$: ≤ 45 символов,
	// чтобы schema_migrations_<Name> уложился в 63 байта идентификатора Postgres.
	Name string
	FS   fs.FS  // embed.FS кита или os.DirFS каталога приложения
	Dir  string // каталог внутри FS с *.up.sql/*.down.sql; "." для os.DirFS
}

// DirSource — источник из каталога на диске.
func DirSource(name, path string) Source {
	return Source{Name: name, FS: os.DirFS(path), Dir: "."}
}

// VersionTable — имя таблицы версий источника.
func (s Source) VersionTable() string { return "schema_migrations_" + s.Name }

var sourceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,44}$`)

func (s Source) validate() error {
	if !sourceNameRe.MatchString(s.Name) {
		return fmt.Errorf("pgkit: migrate: invalid source name %q", s.Name)
	}
	if s.FS == nil {
		return fmt.Errorf("pgkit: migrate: source %q: nil FS", s.Name)
	}
	return nil
}

// MigrateUp применяет все неприменённые миграции источника. Дедлайн — забота
// вызывающего: golang-migrate ctx не принимает.
func MigrateUp(ctx context.Context, dsn string, src Source) error {
	return withMigrator(ctx, dsn, src, func(m *migrate.Migrate) error {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("up: %w", err)
		}
		return nil
	})
}

// MigrateDown откатывает ровно одну миграцию источника.
func MigrateDown(ctx context.Context, dsn string, src Source) error {
	return withMigrator(ctx, dsn, src, func(m *migrate.Migrate) error {
		if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("down: %w", err)
		}
		return nil
	})
}

// MigrateVersion возвращает применённую версию источника (0 — ничего не
// применено); dirty=true приходит вместе с ошибкой.
func MigrateVersion(ctx context.Context, dsn string, src Source) (version uint, dirty bool, err error) {
	err = withMigrator(ctx, dsn, src, func(m *migrate.Migrate) error {
		v, d, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("version: %w", err)
		}
		version, dirty = v, d
		if d {
			return fmt.Errorf("version %d is dirty, manual repair required", v)
		}
		return nil
	})
	return version, dirty, err
}

func withMigrator(ctx context.Context, dsn string, src Source, fn func(*migrate.Migrate) error) error {
	m, err := newMigrator(ctx, dsn, src)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := fn(m); err != nil {
		return fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	return nil
}

func newMigrator(ctx context.Context, dsn string, src Source) (*migrate.Migrate, error) {
	if err := src.validate(); err != nil {
		return nil, err
	}
	srcDrv, err := iofs.New(src.FS, src.Dir)
	if err != nil {
		return nil, fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	if _, err := srcDrv.First(); err != nil {
		return nil, fmt.Errorf("pgkit: migrate source %q: no migrations in %q", src.Name, src.Dir)
	}

	connCfg, err := migrateConnConfig(ctx, dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*connCfg)
	dbDrv, err := migratepgx.WithInstance(db, &migratepgx.Config{MigrationsTable: src.VersionTable()})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pgkit: migrate source %q: connect: %w", src.Name, err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDrv, "pgx5", dbDrv)
	if err != nil {
		_ = dbDrv.Close()
		return nil, fmt.Errorf("pgkit: migrate source %q: %w", src.Name, err)
	}
	return m, nil
}

// Дедлайн ctx становится таймаутом коннекта: сам migrate открывает соединение
// на context.Background().
func migrateConnConfig(ctx context.Context, dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgkit: migrate: parse dsn: %w", err)
	}
	// Файлы миграций многооператорные — их принимает только simple protocol.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("pgkit: migrate: %w", ctx.Err())
		}
		cfg.ConnectTimeout = remaining
	}
	return cfg, nil
}
