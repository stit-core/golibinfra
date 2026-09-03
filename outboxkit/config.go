package outboxkit

import (
	"errors"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// PublishMode задаёт компромисс «порядок vs пропускная способность» на хопе
// worker→брокер.
//
//	batch   — async-батч (один round-trip на тик); порядок персиста best-effort.
//	ordered — последовательная публикация со стопом на первой ошибке; строгий
//	          порядок per-subject ценой round-trip на сообщение.
type PublishMode string

const (
	PublishBatch   PublishMode = "batch"
	PublishOrdered PublishMode = "ordered"
)

type Config struct {
	BatchSize   int           `envconfig:"OUTBOX_BATCH_SIZE"   default:"100"`
	Interval    time.Duration `envconfig:"OUTBOX_INTERVAL"     default:"1s"`
	PublishMode PublishMode   `envconfig:"OUTBOX_PUBLISH_MODE" default:"batch"`
	// MaxAttempts <= 0 — без ограничения; иначе после N неудачных попыток строка
	// помечается dead и больше не публикуется.
	MaxAttempts int `envconfig:"OUTBOX_MAX_ATTEMPTS" default:"0"`
	// TickTimeout — дедлайн одного тика (транзакция + публикация); 0 — без дедлайна.
	TickTimeout time.Duration `envconfig:"OUTBOX_TICK_TIMEOUT" default:"30s"`
}

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	switch c.PublishMode {
	case PublishBatch, PublishOrdered:
	default:
		return fmt.Errorf("outboxkit: invalid OUTBOX_PUBLISH_MODE %q (want batch|ordered)", c.PublishMode)
	}
	if c.BatchSize < 1 {
		return errors.New("outboxkit: OUTBOX_BATCH_SIZE must be >= 1")
	}
	if c.Interval <= 0 {
		return errors.New("outboxkit: OUTBOX_INTERVAL must be > 0")
	}
	if c.TickTimeout < 0 {
		return errors.New("outboxkit: OUTBOX_TICK_TIMEOUT must be >= 0")
	}
	return nil
}
