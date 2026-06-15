package outboxkit

import (
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
}

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	switch c.PublishMode {
	case PublishBatch, PublishOrdered:
	default:
		return c, fmt.Errorf("outboxkit: invalid OUTBOX_PUBLISH_MODE %q (want batch|ordered)", c.PublishMode)
	}
	return c, nil
}
