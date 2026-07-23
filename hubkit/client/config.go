package client

// Config — env-конфиг клиента hub API (имена HUB_* — public API).
type Config struct {
	// BaseURL — company-backend (api.hub.stit.tech): companies/projects/visibility.
	BaseURL string `envconfig:"HUB_BASE_URL"`
	// EmployeeBaseURL — employee-backend (id.stit.tech): employees. Пусто →
	// падает на BaseURL (напр. при path-роутинге под одним хостом).
	EmployeeBaseURL string `envconfig:"HUB_EMPLOYEE_BASE_URL"`
	// ServiceToken — m2m Bearer (значение из SERVICE_TOKENS хаба для этого сервиса).
	ServiceToken string `envconfig:"HUB_SERVICE_TOKEN"`
	// ServiceName — идентификатор сервиса-клиента (проставляется в X-Hub-Service).
	ServiceName string `envconfig:"HUB_SERVICE_NAME"`
}

func (c Config) employeeBaseURL() string {
	if c.EmployeeBaseURL != "" {
		return c.EmployeeBaseURL
	}
	return c.BaseURL
}
