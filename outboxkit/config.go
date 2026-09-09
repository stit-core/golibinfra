package outboxkit

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// PublishMode задаёт компромисс «порядок vs пропускная способность» на хопе
// worker→брокер: batch — async-батч (один round-trip на тик), ordered —
// последовательная публикация со стопом на первой ошибке.
type PublishMode string

const (
	// PublishBatch — async-батч, порядок персиста best-effort.
	PublishBatch PublishMode = "batch"
	// PublishOrdered — последовательная публикация, строгий порядок очереди.
	PublishOrdered PublishMode = "ordered"
)

// DefaultBatchSize — размер батча, который NewWorker подставляет вместо
// невалидного (`BatchSize < 1`) в конфиге, собранном не через NewConfig.
const DefaultBatchSize = 100

// DefaultTickTimeout — дедлайн тика, который NewWorker подставляет вместо
// невалидного (`TickTimeout <= 0`) в конфиге, собранном не через NewConfig.
const DefaultTickTimeout = 30 * time.Second

// Config — конфиг orchestration-слоя (Bus + Worker); читается из ENV
// через NewConfig, провайдится caller'ом.
type Config struct {
	// BatchSize — сколько строк воркер вычитывает за тик (LIMIT).
	BatchSize int `envconfig:"OUTBOX_BATCH_SIZE"   default:"100"`
	// Interval — период тика воркера.
	Interval time.Duration `envconfig:"OUTBOX_INTERVAL"     default:"1s"`
	// PublishMode — режим публикации: batch или ordered.
	PublishMode PublishMode `envconfig:"OUTBOX_PUBLISH_MODE" default:"batch"`
	// MaxAttempts — сколько попыток публикации даётся строке; после N-й
	// неудачи строка помечается dead. <= 0 — без ограничения.
	MaxAttempts int `envconfig:"OUTBOX_MAX_ATTEMPTS" default:"0"`
	// TickTimeout — дедлайн одного тика (транзакция + публикация); > 0.
	TickTimeout time.Duration `envconfig:"OUTBOX_TICK_TIMEOUT" default:"30s"`
	// RetryBackoff — пауза перед первым ретраем строки; каждая следующая
	// неудача удваивает её до RetryMaxBackoff. 0 — ретрай на ближайшем тике.
	RetryBackoff time.Duration `envconfig:"OUTBOX_RETRY_BACKOFF" default:"1s"`
	// RetryMaxBackoff — потолок паузы между попытками; 0 — без потолка.
	RetryMaxBackoff time.Duration `envconfig:"OUTBOX_RETRY_MAX_BACKOFF" default:"5m"`
}

// NewConfig читает Config из ENV (`OUTBOX_*`) и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, fmt.Errorf("outboxkit: config: %w", err)
	}
	return c, c.Validate()
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig.
func (c Config) Validate() error {
	switch c.PublishMode {
	case PublishBatch, PublishOrdered:
	default:
		return fmt.Errorf("%w: OUTBOX_PUBLISH_MODE must be batch|ordered, got %q", ErrConfig, c.PublishMode)
	}
	switch {
	case c.BatchSize < 1:
		return fmt.Errorf("%w: OUTBOX_BATCH_SIZE must be >= 1, got %d", ErrConfig, c.BatchSize)
	case c.Interval <= 0:
		return fmt.Errorf("%w: OUTBOX_INTERVAL must be > 0, got %s", ErrConfig, c.Interval)
	case c.TickTimeout <= 0:
		return fmt.Errorf("%w: OUTBOX_TICK_TIMEOUT must be > 0, got %s", ErrConfig, c.TickTimeout)
	case c.RetryBackoff < 0:
		return fmt.Errorf("%w: OUTBOX_RETRY_BACKOFF must be >= 0, got %s", ErrConfig, c.RetryBackoff)
	case c.RetryMaxBackoff < 0:
		return fmt.Errorf("%w: OUTBOX_RETRY_MAX_BACKOFF must be >= 0, got %s", ErrConfig, c.RetryMaxBackoff)
	case c.RetryMaxBackoff > 0 && c.RetryMaxBackoff < c.RetryBackoff:
		return fmt.Errorf("%w: OUTBOX_RETRY_MAX_BACKOFF (%s) must be >= OUTBOX_RETRY_BACKOFF (%s)",
			ErrConfig, c.RetryMaxBackoff, c.RetryBackoff)
	}
	return nil
}

// withDefaults подставляет безопасные значения вместо невалидных: Config,
// собранный литералом мимо NewConfig, не должен молча давать LIMIT 0.
func (c Config) withDefaults() Config {
	if c.BatchSize < 1 {
		c.BatchSize = DefaultBatchSize
	}
	if c.PublishMode != PublishOrdered {
		c.PublishMode = PublishBatch
	}
	if c.TickTimeout <= 0 {
		c.TickTimeout = DefaultTickTimeout
	}
	if c.RetryBackoff < 0 {
		c.RetryBackoff = 0
	}
	if c.RetryMaxBackoff < 0 {
		c.RetryMaxBackoff = 0
	}
	return c
}
