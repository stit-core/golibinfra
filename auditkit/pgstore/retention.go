package pgstore

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/auditkit"
	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/pgkit"
	"git.stit.tech/stit-core/golibinfra/timerkit"
)

// MaxRetentionDays — верхняя граница RetentionConfig.Days (100 лет): дальше срок перестаёт быть
// сроком, а граница отсекает значение, на котором job падал бы каждый прогон.
const MaxRetentionDays = 36500

// RetentionConfig — срок хранения записей audit_log для RetentionModule. Имена полей и
// envconfig-теги — часть public API.
type RetentionConfig struct {
	// Days — возраст записи (по at, часы базы) в днях, после которого job retention её удаляет;
	// дефолта нет: срок хранения аудита выбирают явно.
	Days int `envconfig:"AUDIT_RETENTION_DAYS"`
}

// NewRetentionConfig читает RetentionConfig из ENV и валидирует его.
func NewRetentionConfig() (RetentionConfig, error) {
	var c RetentionConfig
	if err := envconf.Load(&c); err != nil {
		return RetentionConfig{}, fmt.Errorf("%w: %w", auditkit.ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate — Days в [1, MaxRetentionDays], иначе auditkit.ErrConfig: нулевой RetentionConfig
// отвергается, удаление аудита без явного срока хуже упавшего старта.
func (c RetentionConfig) Validate() error {
	if c.Days < 1 || c.Days > MaxRetentionDays {
		return fmt.Errorf("%w: AUDIT_RETENTION_DAYS must be within [1, %d], got %d", auditkit.ErrConfig, MaxRetentionDays, c.Days)
	}
	return nil
}

// RetentionModule — opt-in удаление записей audit_log старше RetentionConfig.Days: job в группе
// timerkit.JobsGroup (после миграций, если pgkit.MigrateModule в графе). Без него аудит хранится
// бессрочно. Нужны Module, RetentionConfig (fx.Provide(pgstore.NewRetentionConfig)) и
// timerkit.Module — без него граф не собирается.
var RetentionModule = fx.Module(
	"auditkit/pgstore/retention",
	timerkit.ProvideJob(
		func(s *Store, cfg RetentionConfig, _ pgkit.Migrated) (timerkit.Job, error) {
			return RetentionJob(s, cfg)
		},
		fx.ParamTags(``, ``, `optional:"true"`),
	),
)

// retentionInterval — период job'а: батчи догоняют отставание за несколько прогонов.
const retentionInterval = time.Hour

// RetentionJob — job retention: раз в час Store.DeleteExpired с cfg; невалидный cfg —
// auditkit.ErrConfig на сборке графа, а не на первом прогоне.
func RetentionJob(s *Store, cfg RetentionConfig) (timerkit.Job, error) {
	if err := cfg.Validate(); err != nil {
		return timerkit.Job{}, err
	}
	return timerkit.Job{
		Name:     "auditkit.retention",
		Interval: retentionInterval,
		Timeout:  retentionInterval,
		Run:      func(ctx context.Context) error { return s.DeleteExpired(ctx, cfg) },
	}, nil
}

// Батч: на разросшейся audit_log один безлимитный DELETE не укладывается в
// PG_STATEMENT_TIMEOUT и отменяется, не удалив ничего.
const retentionBatchSize = 5000

const deleteExpiredSQL = `
DELETE FROM audit_log
WHERE id IN (SELECT id FROM audit_log WHERE at < NOW() - make_interval(days => $1) LIMIT $2)
`

// DeleteExpired удаляет записи audit_log старше cfg.Days по часам базы батчами по
// retentionBatchSize; невалидный cfg — auditkit.ErrConfig без запроса.
func (s *Store) DeleteExpired(ctx context.Context, cfg RetentionConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	for {
		tag, err := s.db.Exec(ctx, deleteExpiredSQL, cfg.Days, retentionBatchSize)
		if err != nil {
			return fmt.Errorf("auditkit/pgstore: retention: %w", err)
		}
		deleted := tag.RowsAffected()
		s.instr.retentionDeleted.Add(ctx, deleted)
		if deleted < retentionBatchSize {
			return nil
		}
	}
}
