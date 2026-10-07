package flagkit

import (
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module не provide'ит Config: его регистрирует caller — App-структурой или
// `fx.Provide(flagkit.NewConfig)`. Store строится на старте и без потребителя: мусор в
// FEATURE_FLAGS валит запуск.
var Module = fx.Module(
	"flagkit",
	fx.Provide(
		fx.Annotate(NewEnvStore, fx.As(new(Store))),
	),
	// Без Invoke конструктор ленив: проверка FEATURE_FLAGS ждала бы первого потребителя Store.
	fx.Invoke(func(Store) {}),
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
