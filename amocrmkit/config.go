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
}

// NewConfig читает Config из окружения. Asymmetry note (см. pgkit/module.go):
// Module не provide'ит Config — caller регистрирует через fx.Provide(amocrmkit.NewConfig).
func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
