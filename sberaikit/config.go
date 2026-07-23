package sberaikit

import "time"

// Config — публичный контракт env. Имена полей/тегов стабильны (попадают в
// README § Стабильность API). Грузится через config-композицию (ENV).
//
// AuthKey — "Authorization key" Sber = base64(client_id:client_secret); идёт в
// заголовок Authorization: Basic при минте OAuth-токена. Scope выбирает тариф
// (GIGACHAT_API_PERS/B2B/CORP). CABundlePath указывает на PEM с Минцифры-CA
// (root + sub) для scoped RootCAs (см. tls.go); пусто → только встроенный bundle.
type Config struct {
	AuthKey      string        `envconfig:"SBER_AUTH_KEY" required:"true"`
	Scope        string        `envconfig:"SBER_SCOPE" default:"GIGACHAT_API_PERS"`
	BaseURL      string        `envconfig:"SBER_BASE_URL" default:"https://gigachat.devices.sberbank.ru/api/v1"`
	OAuthURL     string        `envconfig:"SBER_OAUTH_URL" default:"https://ngw.devices.sberbank.ru:9443/api/v2/oauth"`
	DefaultModel string        `envconfig:"SBER_DEFAULT_MODEL" default:"GigaChat-2"`
	CABundlePath string        `envconfig:"SBER_CA_BUNDLE_PATH" default:""`
	Timeout      time.Duration `envconfig:"SBER_TIMEOUT" default:"60s"`
	MaxRetries   int           `envconfig:"SBER_MAX_RETRIES" default:"3"`
}
