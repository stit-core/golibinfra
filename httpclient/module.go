package httpclient

import (
	"context"
	"fmt"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// OptionGroup — fx-группа опций клиента: всё, что нельзя выразить через ENV
// (hook лимитера, собственный транспорт), приезжает в Module через неё.
const OptionGroup = "httpclient.options"

// Module не provide'ит Config: caller регистрирует его сам
// (`fx.Provide(httpclient.NewConfig)` или из App-структуры).
var Module = fx.Module("httpclient",
	fx.Provide(newClient),
)

// Params — вход Module: Config от caller'а и опции из группы OptionGroup.
type Params struct {
	fx.In

	Config    Config
	Lifecycle fx.Lifecycle
	Options   []Option `group:"httpclient.options"`
}

func newClient(p Params) (*Client, error) {
	c, err := New(p.Config, p.Options...)
	if err != nil {
		return nil, err
	}
	p.Lifecycle.Append(fx.Hook{OnStop: func(context.Context) error { return c.Close() }})
	return c, nil
}

// ProvideOption регистрирует конструктор Option в группе OptionGroup:
// `httpclient.ProvideOption(func(rl *limiter) httpclient.Option { … })`.
func ProvideOption(ctor any) fx.Option {
	return fx.Provide(fx.Annotate(ctor, fx.ResultTags(`group:"`+OptionGroup+`"`)))
}

// NewConfig читает конфиг из `HTTPCLIENT_*`.
func NewConfig() (Config, error) {
	return NewConfigPrefixed("")
}

// NewConfigPrefixed читает конфиг из `<PREFIX>_HTTPCLIENT_*` с fallback на
// голые `HTTPCLIENT_*`: базовая политика на сервис, точечные переопределения на
// upstream (`AMOCRM_HTTPCLIENT_RETRY_MAX_WAIT`).
func NewConfigPrefixed(prefix string) (Config, error) {
	var c Config
	if err := envconfig.Process(prefix, &c); err != nil {
		return Config{}, fmt.Errorf("httpclient: config: %w", err)
	}
	return c, c.Validate()
}
