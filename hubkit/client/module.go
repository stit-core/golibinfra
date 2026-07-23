package client

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/httpclient"
)

// Module провайдит *Client поверх httpclient.Client. Config не provide'ится
// (asymmetry-нота как в pgkit — caller добавляет `fx.Provide(client.NewConfig)`).
// Требует httpclient.Module (или отдельно предоставленный *httpclient.Client).
var Module = fx.Module(
	"hubkit-client",
	fx.Provide(newClient),
)

type clientParams struct {
	fx.In

	Config Config
	Doer   *httpclient.Client `optional:"true"`
}

func newClient(p clientParams) *Client {
	var doer Doer
	if p.Doer != nil {
		doer = p.Doer
	}
	return New(p.Config, doer)
}

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
