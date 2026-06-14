package anthropickit

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV + Vault).
type Config struct {
	APIKey       string `envconfig:"ANTHROPIC_API_KEY" vault:"anthropic_api_key" required:"true"`
	BaseURL      string `envconfig:"ANTHROPIC_BASE_URL" default:"https://api.anthropic.com"`
	DefaultModel string `envconfig:"ANTHROPIC_DEFAULT_MODEL" default:"claude-opus-4-8"`
	MaxRetries   int    `envconfig:"ANTHROPIC_MAX_RETRIES" default:"2"`
}
