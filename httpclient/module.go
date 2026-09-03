package httpclient

import (
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
)

// Module не provide'ит Config: caller регистрирует его сам
// (`fx.Provide(httpclient.NewConfig)` или из App-структуры).
var Module = fx.Module("httpclient",
	fx.Provide(func(cfg Config) (*Client, error) { return New(cfg) }),
)

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}
