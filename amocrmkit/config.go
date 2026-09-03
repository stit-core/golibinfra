package amocrmkit

import (
	"fmt"
	"net/url"

	"github.com/kelseyhightower/envconfig"
)

// Config — настройки кита. Только то, что одинаково для всех аккаунтов;
// client_id/client_secret/токены/channel-secret — per-account, приходят
// аргументами вызовов (живут в token-store потребителя), не здесь. Таймауты,
// retry и circuit-breaker — в httpclient.Config, которую принимает New.
type Config struct {
	RateRPS int `envconfig:"AMOCRM_RATE_RPS" default:"7"` // потолок amoCRM на аккаунт
	// HostOverride — если задан, все запросы идут на этот scheme://host вместо
	// https://<subdomain>.amocrm.<tld> (путь/query/заголовки/подпись сохраняются).
	// Для sandbox/proxy/e2e против fake-amo. Пусто в проде — host из subdomain/tld.
	HostOverride string `envconfig:"AMOCRM_HOST_OVERRIDE" default:""`
}

func (c Config) Validate() error {
	if c.RateRPS <= 0 {
		return fmt.Errorf("%w: RateRPS must be > 0, got %d", ErrConfig, c.RateRPS)
	}
	if c.HostOverride != "" {
		if _, err := parseHostOverride(c.HostOverride); err != nil {
			return err
		}
	}
	return nil
}

func parseHostOverride(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: HostOverride must be scheme://host, got %q", ErrConfig, s)
	}
	return u, nil
}

// NewConfig читает Config из окружения. Asymmetry note (см. pgkit/module.go):
// Module не provide'ит Config — caller регистрирует через fx.Provide(amocrmkit.NewConfig).
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
