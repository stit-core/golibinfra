package client

import (
	"fmt"
	"net/url"
)

// Config — env-конфиг клиента hub API (имена HUB_* — public API).
type Config struct {
	// BaseURL — company-backend (api.hub.stit.tech): companies/projects/visibility.
	BaseURL string `envconfig:"HUB_BASE_URL"`
	// EmployeeBaseURL — employee-backend (id.stit.tech): employees. Пусто →
	// падает на BaseURL (напр. при path-роутинге под одним хостом).
	EmployeeBaseURL string `envconfig:"HUB_EMPLOYEE_BASE_URL"`
	// TagBaseURL — tag-backend (tags.hub.stit.tech): глобальный словарь тегов
	// (upsert + sync-фид tags). Пусто → падает на BaseURL.
	TagBaseURL string `envconfig:"HUB_TAG_BASE_URL"`
	// ServiceToken — m2m Bearer (значение из SERVICE_TOKENS хаба для этого сервиса).
	ServiceToken string `envconfig:"HUB_SERVICE_TOKEN"`
	// ServiceName — идентификатор сервиса-клиента (проставляется в X-Hub-Service).
	ServiceName string `envconfig:"HUB_SERVICE_NAME"`
}

// Validate проверяет адреса и m2m-токен: без них каждый вызов возвращал бы
// ErrUnavailable/ErrUnauthorized, и потерянная переменная деплоя выглядела бы
// как простой хаба.
func (c Config) Validate() error {
	if err := baseURL("HUB_BASE_URL", c.BaseURL, true); err != nil {
		return err
	}
	if err := baseURL("HUB_EMPLOYEE_BASE_URL", c.EmployeeBaseURL, false); err != nil {
		return err
	}
	if err := baseURL("HUB_TAG_BASE_URL", c.TagBaseURL, false); err != nil {
		return err
	}
	if c.ServiceToken == "" {
		return fmt.Errorf("%w: HUB_SERVICE_TOKEN is required", ErrConfig)
	}
	return nil
}

func baseURL(name, raw string, required bool) error {
	if raw == "" {
		if required {
			return fmt.Errorf("%w: %s is required", ErrConfig, name)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrConfig, name, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: %s must be an absolute http(s) URL", ErrConfig, name)
	}
	return nil
}

func (c Config) employeeBaseURL() string {
	if c.EmployeeBaseURL != "" {
		return c.EmployeeBaseURL
	}
	return c.BaseURL
}

func (c Config) tagBaseURL() string {
	if c.TagBaseURL != "" {
		return c.TagBaseURL
	}
	return c.BaseURL
}
