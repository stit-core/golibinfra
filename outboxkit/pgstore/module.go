package pgstore

import (
	"context"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// Module wires:
//   - Store as outboxkit.Repository (used by outboxkit.Bus/Worker in root Module).
//   - PartitionKeeperJob — periodically creates next-month partitions so inserts
//     don't fail when the calendar rolls into a yet-uncreated partition. Initial
//     migration creates current month + 3 ahead; keeper runs daily, idempotent.
var Module = fx.Module(
	"outboxkit/pgstore",
	fx.Provide(
		NewStore,
		fx.Annotate(func(s *Store) outboxkit.Repository { return s }),
		fx.Annotate(PartitionKeeperJob, fx.ResultTags(`group:"timer.job"`)),
	),
)

func PartitionKeeperJob(s *Store) timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.partition-keeper",
		Interval: 24 * time.Hour,
		Run:      func(ctx context.Context) error { return s.EnsurePartitions(ctx, 1) },
	}
}
