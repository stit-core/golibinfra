package client

import (
	"fmt"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Module провайдит *Client поверх httpclient.Client. Config не provide'ится
// (asymmetry-нота как в pgkit — caller добавляет `fx.Provide(client.NewConfig)`).
// *httpclient.Client в графе опционален: без него клиент собирает свой с дефолтами.
var Module = fx.Module(
	"hubkit/client",
	fx.Provide(newClient),
)

type clientParams struct {
	fx.In

	Config Config
	Doer   *httpclient.Client `optional:"true"`
}

func newClient(p clientParams) (*Client, error) {
	var doer Doer
	if p.Doer != nil {
		doer = p.Doer
	}
	return New(p.Config, doer)
}

// NewConfig читает HUB_* клиента из ENV и валидирует их (ErrConfig на старте,
// а не ErrUnavailable на каждом запросе).
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}
