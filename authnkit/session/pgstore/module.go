package pgstore

import (
	"context"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/authnkit/session"
	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — fx-вход кита: *Store как session.Store, проверка схемы в OnStart (после
// pgkit.MigrateModule, если он в графе; pgkit.CheckSchemaOnStart), миграции в группе
// pgkit.migrations и cleanup-job в группе timerkit.JobsGroup. Нужны *pgkit.DB (pgkit.Module),
// session.Config (fx.Provide(session.NewConfig)) и timerkit.Module; clockkit.Clock
// из графа — опционально.
var Module = fx.Module(
	"authnkit/session/pgstore",
	fx.Provide(
		newStore,
		func(s *Store) session.Store { return s },
	),
	pgkit.CheckSchemaOnStart(Migrations),
	// Зависимость от pgkit.Migrated ставит хук job'ов в очередь после миграций.
	timerkit.ProvideJob(
		func(s *Store, cfg session.Config, _ pgkit.Migrated) timerkit.Job { return CleanupJob(s, cfg) },
		fx.ParamTags(``, ``, `optional:"true"`),
	),
	pgkit.SupplyMigrations(Migrations),
)

type storeParams struct {
	fx.In

	DB    *pgkit.DB
	Cfg   session.Config
	Clock clockkit.Clock `optional:"true"`
}

// NewStore вариадический, fx.Provide его не аннотирует — в графе он живёт обёрткой.
func newStore(p storeParams) (*Store, error) {
	return NewStore(p.Cfg, p.DB, WithClock(p.Clock))
}

// CleanupJob — периодическая очистка сессий с истёкшим абсолютным сроком с
// интервалом Config.CleanupInterval.
func CleanupJob(s *Store, cfg session.Config) timerkit.Job {
	return timerkit.Job{
		Name:     "authnkit.session.cleanup",
		Interval: cfg.CleanupInterval,
		Timeout:  cfg.CleanupInterval,
		Run:      func(ctx context.Context) error { return s.Cleanup(ctx) },
	}
}
