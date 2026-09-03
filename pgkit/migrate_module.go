package pgkit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

type MigrateRunnerConfig struct {
	// Path — каталог миграций приложения; пустая строка — приложение своих
	// миграций не имеет, применяются только источники китов.
	Path string `envconfig:"PG_MIGRATIONS_PATH" default:"/migrations"`
}

func NewMigrateRunnerConfig() (MigrateRunnerConfig, error) {
	var c MigrateRunnerConfig
	return c, envconfig.Process("", &c)
}

// MigrationsGroup — fx-группа источников китов, которые MigrateModule применяет
// вместе с каталогом приложения.
const MigrationsGroup = "pgkit.migrations"

// AppSourceName — имя источника каталога приложения (таблица версий
// schema_migrations_app).
const AppSourceName = "app"

// ProvideMigrations кладёт источник кита в группу MigrationsGroup.
func ProvideMigrations(src Source) fx.Option {
	return fx.Supply(fx.Annotated{Group: MigrationsGroup, Target: src})
}

// MigrateModule прогоняет миграции в OnStart: источники китов по имени, затем
// каталог приложения. Нужны PoolConfig и MigrateRunnerConfig от caller'а.
var MigrateModule = fx.Module("pgkit-migrate",
	fx.Invoke(runMigrationsOnStart),
)

type migrateParams struct {
	fx.In
	LC      fx.Lifecycle
	Pool    PoolConfig
	Runner  MigrateRunnerConfig
	Sources []Source `group:"pgkit.migrations"`
}

func runMigrationsOnStart(p migrateParams) {
	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			sources := startupSources(p.Sources, p.Runner.Path)
			if err := MigrateUpAll(ctx, p.Pool.DSN, sources...); err != nil {
				return fmt.Errorf("pgkit: migrate on start: %w", err)
			}
			return nil
		},
	})
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
