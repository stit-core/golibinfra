package amocrmkit

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config — настройки kit'а. Только то, что одинаково для всех аккаунтов;
// client_id/client_secret/токены/channel-secret — per-account, приходят
// аргументами вызовов (живут в token-store gateway), не здесь.
type Config struct {
	Timeout time.Duration `envconfig:"AMOCRM_TIMEOUT" default:"10s"`
	RateRPS int           `envconfig:"AMOCRM_RATE_RPS" default:"7"` // потолок amoCRM на аккаунт
	// HostOverride — если задан, REST-вызовы идут на этот scheme://host вместо
	// https://<subdomain>.amocrm.<tld> (путь/заголовки/подпись сохраняются). Для
	// sandbox/proxy/e2e против fake-amo. Пусто в проде — host из subdomain/tld.
	HostOverride string `envconfig:"AMOCRM_HOST_OVERRIDE" default:""`
}

// NewConfig читает Config из окружения. Asymmetry note (см. pgkit/module.go):
// Module не provide'ит Config — caller регистрирует через fx.Provide(amocrmkit.NewConfig).
func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
