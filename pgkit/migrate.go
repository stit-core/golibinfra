package pgkit

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

type MigrateConfig struct {
	DSN  string
	Path string
}

func MigrateUp(cfg MigrateConfig) error {
	m, err := newMigrator(cfg)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("pgkit: migrate up: %w", err)
	}
	return nil
}

func MigrateDown(cfg MigrateConfig) error {
	m, err := newMigrator(cfg)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("pgkit: migrate down: %w", err)
	}
	return nil
}

func MigrateVersion(cfg MigrateConfig) (uint, bool, error) {
	m, err := newMigrator(cfg)
	if err != nil {
		return 0, false, err
	}
	defer m.Close()

	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("pgkit: migrate version: %w", err)
	}
	if dirty {
		return v, true, fmt.Errorf("pgkit: миграция %d помечена dirty — нужна ручная починка", v)
	}
	return v, false, nil
}

func newMigrator(cfg MigrateConfig) (*migrate.Migrate, error) {
	m, err := migrate.New("file://"+cfg.Path, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgkit: open migrator: %w", err)
	}
	return m, nil
}
