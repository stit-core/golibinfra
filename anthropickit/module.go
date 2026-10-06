package anthropickit

import (
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module provide'ит anthropickit.API. Config собирает приложение: дешёвый путь —
// fx.Provide(anthropickit.NewConfig). Опции (WithPriceTable и т.п.) — через свой
// конструктор поверх New. Health-проба opt-in: healthkit.Provide(anthropickit.Probe).
var Module = fx.Module(
	"anthropickit",
	fx.Provide(
		fx.Annotate(newClient, fx.As(new(API))),
	),
)

// NewConfig грузит Config из окружения и валидирует её.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

func newClient(cfg Config, log logkit.Logger) (*Client, error) {
	return New(cfg, log)
}
