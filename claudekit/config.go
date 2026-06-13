package claudekit

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV + Vault).
type Config struct {
	APIKey       string `envconfig:"CLAUDE_API_KEY" vault:"claude_api_key" required:"true"`
	BaseURL      string `envconfig:"CLAUDE_BASE_URL" default:"https://api.anthropic.com"`
	DefaultModel string `envconfig:"CLAUDE_DEFAULT_MODEL" default:"claude-opus-4-8"`
	MaxRetries   int    `envconfig:"CLAUDE_MAX_RETRIES" default:"2"`
}
