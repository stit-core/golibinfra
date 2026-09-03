package pgstore

import (
	"context"
	"time"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module — single-instance путь (таблица DefaultTable); Config провайдит
// caller: `fx.Provide(pgstore.NewConfig)`.
var Module = fx.Module(
	"outboxkit/pgstore",
	fx.Provide(
		newStoreFromConfig,
		fx.Annotate(func(s *Store) outboxkit.Repository { return s }),
		fx.Annotate(PartitionKeeperJob, fx.ResultTags(`group:"timer.job"`)),
		fx.Annotate(
			func(s *Store, cfg Config) timerkit.Job { return RetentionKeeperJob(s, cfg.RetentionMonths) },
			fx.ResultTags(`group:"timer.job"`),
		),
	),
	pgkit.ProvideMigrations(Migrations),
	fx.Invoke(ensurePartitionsOnStart),
)

type Config struct {
	RetentionMonths int  `envconfig:"OUTBOX_RETENTION_MONTHS" default:"0"`
	RequireTx       bool `envconfig:"OUTBOX_REQUIRE_TX" default:"false"`
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newStoreFromConfig(db *pgkit.DB, cfg Config) *Store {
	var opts []StoreOption
	if cfg.RequireTx {
		opts = append(opts, WithRequireTx())
	}
	return NewStore(db, opts...)
}

// ensurePartitionsOnStart: timer-job впервые срабатывает через сутки, а сервис
// может не прожить столько — партиции нужны с первого insert'а.
func ensurePartitionsOnStart(lc fx.Lifecycle, s *Store) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error { return s.EnsurePartitions(ctx, 1) },
	})
}

func PartitionKeeperJob(s *Store) timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.partition-keeper." + s.table,
		Interval: 24 * time.Hour,
		Run:      func(ctx context.Context) error { return s.EnsurePartitions(ctx, 1) },
	}
}

func RetentionKeeperJob(s *Store, keepMonths int) timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.retention." + s.table,
		Interval: 24 * time.Hour,
		Run:      func(ctx context.Context) error { return s.DropOldPartitions(ctx, keepMonths) },
	}
}
