package httpclient

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module не provide'ит Config: caller регистрирует его сам
// (`fx.Provide(httpclient.NewConfig)` или из App-структуры).
var Module = fx.Module("httpclient",
	fx.Provide(newClient),
)

// Options — опции клиента в графе одним значением: всё, что нельзя выразить через ENV
// (hook лимитера, подпись, собственный транспорт). Применяются в порядке слайса; одно
// значение на граф — порядок задаёт приложение.
type Options []Option

// Params — вход Module: Config от caller'а и опциональные Options.
type Params struct {
	fx.In

	Config    Config
	Lifecycle fx.Lifecycle
	Options   Options `optional:"true"`
}

func newClient(p Params) (*Client, error) {
	c, err := New(p.Config, p.Options...)
	if err != nil {
		return nil, err
	}
	p.Lifecycle.Append(fx.Hook{OnStop: func(context.Context) error { return c.Close() }})
	return c, nil
}

// NewConfig читает конфиг из `HTTPCLIENT_*`.
func NewConfig() (Config, error) {
	return NewConfigPrefixed("")
}

// NewConfigPrefixed читает конфиг из `<PREFIX>_HTTPCLIENT_*` (breaker — из
// `<PREFIX>_HTTPCLIENT_BREAKER_*`) с fallback на голые `HTTPCLIENT_*`: базовая политика на
// сервис, точечные переопределения на upstream (`AMOCRM_HTTPCLIENT_RETRY_MAX_WAIT`).
func NewConfigPrefixed(prefix string) (Config, error) {
	var c Config
	if err := envconf.LoadPrefixed(&c, prefix, true); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
