package config

import "go.uber.org/fx"

// Module провайдит в граф []Source с единственным источником — Env();
// секреты приложение получает обычными переменными окружения.
var Module = fx.Module(
	"config",
	fx.Provide(newSources),
)

func newSources() []Source {
	return []Source{Env()}
}
