package amocrmkit

import (
	"fmt"
	"net/url"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// defaultMaxWait — потолок ожидания слота, если MaxWait не задан.
const defaultMaxWait = 5 * time.Second

// Config — настройки кита: только то, что одинаково для всех аккаунтов.
// Per-account-секреты (client_id/client_secret, токены, channel-secret) приходят
// аргументами вызовов. Таймауты, retry и circuit-breaker — в httpclient.Config,
// которую принимает New.
type Config struct {
	RateRPS int `envconfig:"AMOCRM_RATE_RPS" default:"7"` // потолок amoCRM на аккаунт
	// MaxWait — потолок ожидания слота лимитера аккаунта (очередь + пауза после
	// 429). Дольше — вызов отклоняется локально с ErrRateLimited, не занимая
	// горутину на всю паузу.
	MaxWait time.Duration `envconfig:"AMOCRM_MAX_WAIT" default:"5s"`
	// HostOverride — если задан, все запросы идут на этот scheme://host вместо
	// https://<subdomain>.amocrm.<tld> (путь/query/заголовки/подпись сохраняются).
	// Для sandbox/proxy/e2e против fake-amo. Пусто в проде — host из subdomain/tld.
	HostOverride string `envconfig:"AMOCRM_HOST_OVERRIDE" default:""`
}

// Validate отвергает невалидные значения (ErrConfig) и подставляет дефолт
// вместо нулевого MaxWait: собранный вручную Config не ждёт слот бесконечно.
func (c *Config) Validate() error {
	if c.RateRPS <= 0 {
		return fmt.Errorf("%w: AMOCRM_RATE_RPS must be > 0, got %d", ErrConfig, c.RateRPS)
	}
	if c.MaxWait < 0 {
		return fmt.Errorf("%w: AMOCRM_MAX_WAIT must be >= 0, got %s", ErrConfig, c.MaxWait)
	}
	if c.MaxWait == 0 {
		c.MaxWait = defaultMaxWait
	}
	if c.HostOverride != "" {
		if _, err := parseHostOverride(c.HostOverride); err != nil {
			return err
		}
	}
	return nil
}

// parseHostOverride принимает только scheme://host: путь и query у override
// молча потерялись бы при подстановке в адрес вызова.
func parseHostOverride(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, fmt.Errorf("%w: HostOverride must be scheme://host, got %q", ErrConfig, s)
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.User != nil:
		return nil, fmt.Errorf("%w: HostOverride must carry no path, query or userinfo, got %q", ErrConfig, s)
	}
	return u, nil
}

// NewConfig читает Config из окружения и валидирует его. Module Config не
// provide'ит — caller регистрирует fx.Provide(amocrmkit.NewConfig).
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
