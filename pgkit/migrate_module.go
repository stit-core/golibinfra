package pgkit

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// MigrateRunnerConfig — параметры прогона миграций в OnStart (MigrateModule).
type MigrateRunnerConfig struct {
	// Path — каталог миграций приложения; пустая строка (в ENV — «-») — своих миграций у
	// приложения нет, применяются только источники китов.
	Path    string        `envconfig:"PG_MIGRATIONS_PATH" default:"/migrations"`
	Timeout time.Duration `envconfig:"PG_MIGRATE_TIMEOUT" default:"2m"`
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig.
func (c MigrateRunnerConfig) Validate() error {
	if c.Path != strings.TrimSpace(c.Path) {
		return fmt.Errorf("%w: PG_MIGRATIONS_PATH has surrounding whitespace: %q", ErrConfig, c.Path)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("%w: PG_MIGRATE_TIMEOUT must be > 0, got %s", ErrConfig, c.Timeout)
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

// ProvideMigrations кладёт источник кита в группу MigrationsGroup.
func ProvideMigrations(src Source) fx.Option {
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

			sources := startupSources(p.Sources, p.Runner.Path)
			if err := MigrateUpAll(ctx, p.Pool.DSN, sources...); err != nil {
				return fmt.Errorf("pgkit: migrate on start: %w", err)
			}
			return nil
		},
	})
	return Migrated{}, nil
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
