package logkit

import "context"

type ServiceConfig struct {
	Service string `envconfig:"OTEL_SERVICE_NAME"`
	Env     string `envconfig:"APP_ENV" default:"dev"`
	Version string `envconfig:"OTEL_SERVICE_VERSION" default:"dev"`
}

func registerServiceAttrs(cfg ServiceConfig) {
	RegisterAttr(func(_ context.Context) (string, any, bool) {
		return "service", cfg.Service, true
	})
	RegisterAttr(func(_ context.Context) (string, any, bool) {
		return "env", cfg.Env, true
	})
	RegisterAttr(func(_ context.Context) (string, any, bool) {
		return "version", cfg.Version, true
	})
}
