package anthropickit

import (
	"fmt"
	"net/url"
	"time"
)

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV) или NewConfig.
type Config struct {
	APIKey       string        `envconfig:"ANTHROPIC_API_KEY" required:"true"`
	BaseURL      string        `envconfig:"ANTHROPIC_BASE_URL" default:"https://api.anthropic.com"`
	DefaultModel string        `envconfig:"ANTHROPIC_DEFAULT_MODEL" default:"claude-opus-5"`
	MaxRetries   int           `envconfig:"ANTHROPIC_MAX_RETRIES" default:"2"`
	Timeout      time.Duration `envconfig:"ANTHROPIC_TIMEOUT" default:"10m"`
}

// Validate возвращает первое нарушение как ErrConfig: неработоспособный конфиг
// падает на старте, а не на первом вызове API.
func (c Config) Validate() error {
	switch {
	case c.APIKey == "":
		return fmt.Errorf("%w: ANTHROPIC_API_KEY is required", ErrConfig)
	case c.DefaultModel == "":
		return fmt.Errorf("%w: ANTHROPIC_DEFAULT_MODEL is required", ErrConfig)
	case c.MaxRetries < 0:
		return fmt.Errorf("%w: ANTHROPIC_MAX_RETRIES must be >= 0, got %d", ErrConfig, c.MaxRetries)
	case c.Timeout <= 0:
		return fmt.Errorf("%w: ANTHROPIC_TIMEOUT must be > 0, got %s", ErrConfig, c.Timeout)
	}
	return validateBaseURL(c.BaseURL)
}

func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: ANTHROPIC_BASE_URL must be http(s)://host, got %q", ErrConfig, raw)
	}
	return nil
}
