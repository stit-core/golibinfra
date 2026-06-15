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

// Module wires the single-instance (table `outbox`) path:
//   - Store as outboxkit.Repository (used by outboxkit.Bus/Worker in root Module).
//   - PartitionKeeperJob — заранее создаёт партиции следующего месяца, чтобы
//     inserts не падали на смене календаря.
//   - RetentionKeeperJob — дропает партиции старше OUTBOX_RETENTION_MONTHS
//     (0 = не дропать). Оба идемпотентны, бегут раз в сутки.
var Module = fx.Module(
	"outboxkit/pgstore",
	fx.Provide(
		newStoreFromConfig,
		NewConfig,
		fx.Annotate(func(s *Store) outboxkit.Repository { return s }),
		fx.Annotate(PartitionKeeperJob, fx.ResultTags(`group:"timer.job"`)),
		fx.Annotate(
			func(s *Store, cfg Config) timerkit.Job { return RetentionKeeperJob(s, cfg.RetentionMonths) },
			fx.ResultTags(`group:"timer.job"`),
		),
	),
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
