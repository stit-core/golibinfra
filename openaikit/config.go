package openaikit

import "time"

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV).
//
// BaseURL/Organization/Project инжектируемы для Azure-готовности и проектных
// ключей (sk-proj-* несут org+project неявно, но legacy-ключам нужны заголовки).
type Config struct {
	APIKey       string        `envconfig:"OPENAI_API_KEY" required:"true"`
	BaseURL      string        `envconfig:"OPENAI_BASE_URL" default:"https://api.openai.com/v1"`
	Organization string        `envconfig:"OPENAI_ORG_ID"`
	Project      string        `envconfig:"OPENAI_PROJECT_ID"`
	DefaultModel string        `envconfig:"OPENAI_DEFAULT_MODEL" default:"gpt-5.5"`
	MaxRetries   int           `envconfig:"OPENAI_MAX_RETRIES" default:"2"`
	Timeout      time.Duration `envconfig:"OPENAI_TIMEOUT" default:"120s"` // SDK без дефолт-таймаута — выставляем здесь
}
