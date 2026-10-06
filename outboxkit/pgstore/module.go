package pgstore

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/internal/envconf"
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
		func(s *Store) outboxkit.Repository { return s },
	),
	// Зависимость от pgkit.Migrated ставит хуки job'ов в очередь после миграций:
	// RunOnStart иначе ловит отсутствующую таблицу на чистой БД.
	timerkit.ProvideJob(
		func(s *Store, _ pgkit.Migrated) timerkit.Job { return PartitionKeeperJob(s) },
		fx.ParamTags(``, `optional:"true"`),
	),
	timerkit.ProvideJob(
		func(s *Store, cfg Config, _ pgkit.Migrated) timerkit.Job {
			return RetentionKeeperJob(s, cfg.RetentionMonths)
		},
		fx.ParamTags(``, ``, `optional:"true"`),
	),
	pgkit.SupplyMigrations(Migrations),
	fx.Invoke(ensurePartitionsOnStart),
)

// FactoryModule провайдит outboxkit.RepositoryFactory для outboxkit.Instance —
// multi-instance путь; сочетается с Module (таблица DefaultTable).
var FactoryModule = fx.Module(
	"outboxkit/pgstore/factory",
	fx.Provide(NewRepositoryFactory),
)

// Config — конфиг Postgres-store'а outbox'а; читается из ENV через NewConfig.
type Config struct {
	// RetentionMonths — сколько месяцев хранить партиции; 0 — не дропать.
	RetentionMonths int `envconfig:"OUTBOX_RETENTION_MONTHS" default:"0"`
	// RequireTx — требовать активную транзакцию в Insert (tx-strict guard).
	RequireTx bool `envconfig:"OUTBOX_REQUIRE_TX" default:"true"`
}

// Validate проверяет инварианты конфига; нарушение — outboxkit.ErrConfig.
func (c Config) Validate() error {
	if c.RetentionMonths < 0 {
		return fmt.Errorf("%w: OUTBOX_RETENTION_MONTHS must be >= 0, got %d", outboxkit.ErrConfig, c.RetentionMonths)
	}
	return nil
}

// NewConfig читает Config из ENV (`OUTBOX_RETENTION_MONTHS`, `OUTBOX_REQUIRE_TX`)
// и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", outboxkit.ErrConfig, err)
	}
	return c, c.Validate()
}

// storeParams: часы опциональны — без clockkit.Module берётся clockkit.Real.
type storeParams struct {
	fx.In

	DB    *pgkit.DB
	Cfg   Config
	Clock clockkit.Clock `optional:"true"`
}

func (p storeParams) options() []StoreOption {
	opts := []StoreOption{WithRetention(p.Cfg.RetentionMonths), WithClock(p.Clock)}
	if p.Cfg.RequireTx {
		opts = append(opts, WithRequireTx())
	}
	return opts
}

func newStoreFromConfig(p storeParams) (*Store, error) {
	if err := p.Cfg.Validate(); err != nil {
		return nil, err
	}
	return NewStore(p.DB, p.options()...), nil
}

// NewRepositoryFactory строит фабрику Store'ов на произвольные таблицы для
// outboxkit.Instance: конфиг (tx-guard, retention) и часы у всех инстансов общие,
// схема таблицы — TableMigrations.
func NewRepositoryFactory(p storeParams) (outboxkit.RepositoryFactory, error) {
	if err := p.Cfg.Validate(); err != nil {
		return nil, err
	}
	return repositoryFactory{db: p.DB, opts: p.options()}, nil
}

type repositoryFactory struct {
	db   *pgkit.DB
	opts []StoreOption
}

func (f repositoryFactory) Repository(table string) (outboxkit.Repository, error) {
	s, err := NewStoreFor(f.db, table, f.opts...)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (repositoryFactory) Migrations(table string) pgkit.Source { return TableMigrations(table) }

// partitionParams: зависимость от pgkit.Migrated ставит хук в очередь после
// миграций, не полагаясь на позицию модуля в fx.New; optional — сборка без
// pgkit.MigrateModule (миграции накатывает CI) остаётся рабочей.
type partitionParams struct {
	fx.In

	LC       fx.Lifecycle
	Store    *Store
	Migrated pgkit.Migrated `optional:"true"`
}

// ensurePartitionsOnStart: партиции нужны с первого insert'а, а первый тик keeper'а случится
// позже старта; без миграций старт падает на проверке схемы с именем источника, а не на DDL
// партиции.
func ensurePartitionsOnStart(p partitionParams) {
	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := p.Store.CheckSchema(ctx); err != nil {
				return err
			}
			return p.Store.EnsurePartitions(ctx, 1)
		},
	})
}

// PartitionKeeperJob — суточный job, создающий партицию текущего и следующего
// месяца. RunOnStart: кастомную таблицу (Instance) больше некому подготовить, а
// CREATE TABLE IF NOT EXISTS идемпотентен.
func PartitionKeeperJob(s *Store) timerkit.Job {
	return timerkit.Job{
		Name:       "outboxkit.partition-keeper." + s.table,
		Interval:   24 * time.Hour,
		RunOnStart: true,
		Run:        func(ctx context.Context) error { return s.EnsurePartitions(ctx, 1) },
	}
}

// RetentionKeeperJob — суточный job, дропающий партиции старше keepMonths;
// keepMonths <= 0 — job без работы.
func RetentionKeeperJob(s *Store, keepMonths int) timerkit.Job {
	return timerkit.Job{
		Name:     "outboxkit.retention." + s.table,
		Interval: 24 * time.Hour,
		Run:      func(ctx context.Context) error { return s.DropOldPartitions(ctx, keepMonths) },
	}
}
