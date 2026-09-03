package anthropickit

import (
	"fmt"
	"time"
)

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV).
type Config struct {
	APIKey       string        `envconfig:"ANTHROPIC_API_KEY" required:"true"`
	BaseURL      string        `envconfig:"ANTHROPIC_BASE_URL" default:"https://api.anthropic.com"`
	DefaultModel string        `envconfig:"ANTHROPIC_DEFAULT_MODEL" default:"claude-opus-4-8"`
	MaxRetries   int           `envconfig:"ANTHROPIC_MAX_RETRIES" default:"2"`
	Timeout      time.Duration `envconfig:"ANTHROPIC_TIMEOUT"`
}

func (c Config) Validate() error {
	switch {
	case c.APIKey == "":
		return fmt.Errorf("%w: ANTHROPIC_API_KEY is required", ErrConfig)
	case c.MaxRetries < 0:
		return fmt.Errorf("%w: MaxRetries < 0", ErrConfig)
	case c.Timeout < 0:
		return fmt.Errorf("%w: Timeout < 0", ErrConfig)
	}
	return nil
}
