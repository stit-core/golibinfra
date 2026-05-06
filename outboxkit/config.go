package outboxkit

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	BatchSize int           `envconfig:"OUTBOX_BATCH_SIZE" default:"100"`
	Interval  time.Duration `envconfig:"OUTBOX_INTERVAL"   default:"1s"`
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
