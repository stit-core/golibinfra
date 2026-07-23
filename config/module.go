package config

import "go.uber.org/fx"

// Module провайдит набор Source'ов для config.Load. Единственный источник —
// env (envconfig); секреты приложений приходят обычными env-переменными.
var Module = fx.Module("config",
	fx.Provide(newSources),
)

func newSources() []Source {
	return []Source{Env()}
}
