package pgstore

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — fx-вход кита: *Store как bruteforcekit.Guard и bruteforcekit.Cleaner,
// миграции в группе pgkit.MigrateModule и cleanup-job в группе timerkit.Group.
// Нужны *pgkit.DB (pgkit.Module), bruteforcekit.Config (fx.Provide(bruteforcekit.NewConfig))
// и timerkit.Module — иначе job'ы группы никто не запускает и таблица не чистится.
var Module = fx.Module(
	"bruteforcekit/pgstore",
	fx.Provide(
		NewStore,
		func(s *Store) bruteforcekit.Guard { return s },
		func(s *Store) bruteforcekit.Cleaner { return s },
	),
	timerkit.Provide(CleanupJob),
	pgkit.ProvideMigrations(Migrations),
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
