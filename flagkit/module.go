package flagkit

import (
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module не provide'ит Config: его регистрирует caller — App-структурой или
// `fx.Provide(flagkit.NewConfig)`.
var Module = fx.Module(
	"flagkit",
	fx.Provide(
		fx.Annotate(NewEnvStore, fx.As(new(Store))),
	),
)

// NewConfig читает FEATURE_FLAGS из окружения и валидирует формат: невалидная
// запись роняет старт приложения, а не выключает флаг молча.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
