package pgkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// MigrateRunnerConfig — параметры прогона миграций: MigrateModule и команда миграций потребителя.
type MigrateRunnerConfig struct {
	// Path — каталог миграций приложения; пустая строка (в ENV — «-») — своих миграций у
	// приложения нет, применяются только источники китов.
	Path    string        `envconfig:"PG_MIGRATIONS_PATH" default:"/migrations"`
	Timeout time.Duration `envconfig:"PG_MIGRATE_TIMEOUT" default:"2m"`
	// LockTimeout — lock_timeout соединения мигратора: столько оператор миграции ждёт
	// блокировку и держит за собой очередь запросов к таблице.
	LockTimeout time.Duration `envconfig:"PG_MIGRATE_LOCK_TIMEOUT" default:"5s"`
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig.
func (c MigrateRunnerConfig) Validate() error {
	if c.Path != strings.TrimSpace(c.Path) {
		return fmt.Errorf("%w: PG_MIGRATIONS_PATH has surrounding whitespace: %q", ErrConfig, c.Path)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("%w: PG_MIGRATE_TIMEOUT must be > 0, got %s", ErrConfig, c.Timeout)
	}
	if c.LockTimeout <= 0 {
		return fmt.Errorf("%w: PG_MIGRATE_LOCK_TIMEOUT must be > 0, got %s", ErrConfig, c.LockTimeout)
	}
	return nil
}

// NewMigrateRunnerConfig читает MigrateRunnerConfig из ENV и валидирует его.
func NewMigrateRunnerConfig() (MigrateRunnerConfig, error) {
	var c MigrateRunnerConfig
	if err := envconf.Load(&c); err != nil {
		return MigrateRunnerConfig{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// MigrationsGroup — fx-группа источников китов, которые MigrateModule применяет
// вместе с каталогом приложения.
const MigrationsGroup = "pgkit.migrations"

// AppSourceName — имя источника каталога приложения (таблица версий
// schema_migrations_app).
const AppSourceName = "app"

// Migrated — маркер «миграции применены»: OnStart-хук, которому нужна схема,
// объявляет его зависимостью и попадает в очередь после хука миграций.
type Migrated struct{}

// SupplyMigrations кладёт значение источника кита в группу MigrationsGroup.
func SupplyMigrations(src Source) fx.Option {
	return fx.Supply(fx.Annotated{Group: MigrationsGroup, Target: src})
}

// MigrateModule прогоняет миграции в OnStart: источники китов по имени, затем
// каталог приложения. Нужны PoolConfig и MigrateRunnerConfig от caller'а.
var MigrateModule = fx.Module("pgkit/migrate",
	fx.Provide(provideMigrated),
	fx.Invoke(func(Migrated) {}),
)

type migrateParams struct {
	fx.In
	LC      fx.Lifecycle
	Pool    PoolConfig
	Runner  MigrateRunnerConfig
	Sources []Source `group:"pgkit.migrations"`
}

func provideMigrated(p migrateParams) (Migrated, error) {
	if err := p.Runner.Validate(); err != nil {
		return Migrated{}, err
	}
	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, p.Runner.Timeout)
			defer cancel()

			if err := checkAppDir(p.Runner.Path); err != nil {
				return fmt.Errorf("pgkit: migrate on start: %w", err)
			}
			sources := startupSources(p.Sources, p.Runner.Path)
			if err := MigrateUpAll(ctx, p.Pool.DSN, p.Runner.LockTimeout, sources...); err != nil {
				return fmt.Errorf("pgkit: migrate on start: %w", err)
			}
			return nil
		},
	})
	return Migrated{}, nil
}

// checkAppDir — каталог миграций приложения на месте и не пуст: источник читает его как корень
// «.», и ошибка источника не назвала бы ни каталог, ни переменную.
func checkAppDir(path string) error {
	if path == "" {
		return nil
	}
	const hint = "set PG_MIGRATIONS_PATH=- if the service has no migrations of its own"
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("PG_MIGRATIONS_PATH %q: %w (%s)", path, err, hint)
	}
	if found, _ := filepath.Glob(filepath.Join(path, "*.up.sql")); len(found) == 0 {
		return fmt.Errorf("PG_MIGRATIONS_PATH %q: no *.up.sql migrations in the directory (%s)", path, hint)
	}
	return nil
}

// startupSources: киты по имени (порядок fx-группы не определён), приложение
// последним — его схема может ссылаться на таблицы китов.
func startupSources(kits []Source, appPath string) []Source {
	out := slices.Clone(kits)
	slices.SortFunc(out, func(a, b Source) int { return strings.Compare(a.Name, b.Name) })
	if appPath != "" {
		out = append(out, DirSource(AppSourceName, appPath))
	}
	return out
}
