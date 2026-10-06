package pgstore

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — fx-вход кита: *Store как bruteforcekit.Guard и bruteforcekit.Cleaner,
// миграции в группе pgkit.MigrateModule, проверка схемы в OnStart (после них, если модуль в
// графе) и cleanup-job в группе timerkit.JobsGroup.
// Нужны *pgkit.DB (pgkit.Module), bruteforcekit.Config (fx.Provide(bruteforcekit.NewConfig))
// и timerkit.Module — без него граф не собирается.
var Module = fx.Module(
	"bruteforcekit/pgstore",
	fx.Provide(
		NewStore,
		func(s *Store) bruteforcekit.Guard { return s },
		func(s *Store) bruteforcekit.Cleaner { return s },
	),
	// Зависимость от pgkit.Migrated ставит хук job'ов в очередь после миграций; optional —
	// сборка без pgkit.MigrateModule остаётся рабочей.
	timerkit.ProvideJob(
		func(s *Store, cfg bruteforcekit.Config, _ pgkit.Migrated) timerkit.Job { return CleanupJob(s, cfg) },
		fx.ParamTags(``, ``, `optional:"true"`),
	),
	pgkit.SupplyMigrations(Migrations),
	pgkit.CheckSchemaOnStart(Migrations),
)

// CleanupJob — периодическая чистка протухших строк с интервалом
// Config.CleanupInterval.
func CleanupJob(s *Store, cfg bruteforcekit.Config) timerkit.Job {
	return timerkit.Job{
		Name:     "bruteforcekit.cleanup",
		Interval: cfg.CleanupInterval,
		Timeout:  cfg.CleanupInterval,
		Run:      func(ctx context.Context) error { return s.Cleanup(ctx) },
	}
}
