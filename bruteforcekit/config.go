package bruteforcekit

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config controls the anti-bruteforce thresholds.
//
// Window is reserved for a future sliding-window reset implementation
// (see TODO in pgguard.go). It is currently unused but exposed via env
// so deployments can be configured ahead of the feature landing.
type Config struct {
	MaxFailures  int           `envconfig:"BRUTEFORCE_MAX_FAILURES" default:"5"`
	LockDuration time.Duration `envconfig:"BRUTEFORCE_LOCK_DURATION" default:"15m"`
	Window       time.Duration `envconfig:"BRUTEFORCE_WINDOW" default:"1h"`
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
