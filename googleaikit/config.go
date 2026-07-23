package googleaikit

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV).
//
// Dual-backend: Backend == "vertex" → Vertex AI (Project+Location, ADC/OAuth2);
// иначе Gemini Developer API (AI-Studio, APIKey). На AI-Studio APIKey обязателен;
// на Vertex обязательны Project+Location, креды берутся из ADC.
type Config struct {
	APIKey       string `envconfig:"GOOGLE_API_KEY"`
	Backend      string `envconfig:"GOOGLE_BACKEND" default:"aistudio"`
	Project      string `envconfig:"GOOGLE_PROJECT"`
	Location     string `envconfig:"GOOGLE_LOCATION"`
	BaseURL      string `envconfig:"GOOGLE_BASE_URL"`
	APIVersion   string `envconfig:"GOOGLE_API_VERSION" default:"v1beta"`
	DefaultModel string `envconfig:"GOOGLE_DEFAULT_MODEL" default:"gemini-2.5-flash"`
}

// isVertex — true, если конфиг указывает на Vertex AI backend.
func (c Config) isVertex() bool { return c.Backend == "vertex" || c.Backend == "vertexai" }
