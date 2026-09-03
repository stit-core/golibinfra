package bruteforcekit

import (
	"errors"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	MaxFailures      int           `envconfig:"BRUTEFORCE_MAX_FAILURES" default:"5"`
	LockDuration     time.Duration `envconfig:"BRUTEFORCE_LOCK_DURATION" default:"15m"`
	CleanupRetention time.Duration `envconfig:"BRUTEFORCE_CLEANUP_RETENTION" default:"24h"`
}

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.MaxFailures < 1 {
		return errors.New("bruteforcekit: BRUTEFORCE_MAX_FAILURES must be >= 1")
	}
	if c.LockDuration <= 0 {
		return errors.New("bruteforcekit: BRUTEFORCE_LOCK_DURATION must be > 0")
	}
	if c.CleanupRetention < 0 {
		return errors.New("bruteforcekit: BRUTEFORCE_CLEANUP_RETENTION must be >= 0")
	}
	return nil
}
