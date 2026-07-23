package yandexaikit

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/config"
	"git.stit.tech/stit-core/golibinfra/httpclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go): caller
// регистрирует через fx.Provide(yandexaikit.NewConfig). newClient забирает из
// графа *httpclient.Client, clockkit.Clock и logkit.Logger.
var Module = fx.Module(
	"yandexaikit",
	fx.Provide(
		fx.Annotate(newClient, fx.As(new(API))),
	),
)

// NewConfig читает Config из ENV (envconfig). Секреты (yandex_api_key,
// service-account ключ) приходят обычными env-переменными.
func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

// LoadConfig — композиция ENV + caller-переданных Source'ов через
// golibinfra/config.
func LoadConfig(ctx context.Context, sources ...config.Source) (Config, error) {
	var c Config
	if err := config.Load(ctx, &c, append([]config.Source{config.Env()}, sources...)...); err != nil {
		return Config{}, err
	}
	return c, nil
}

func newClient(cfg Config, hc *httpclient.Client, clock clockkit.Clock, log logkit.Logger) (*Client, error) {
	return New(cfg, hc, clock, log)
}
