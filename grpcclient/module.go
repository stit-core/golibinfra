package grpcclient

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"
	"google.golang.org/grpc"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go). Дешёвый mode:
// fx.Provide(grpcclient.NewConfig). Провайдит один *grpc.ClientConn к
// GRPC_CLIENT_TARGET; для нескольких таргетов — Dial напрямую.
var Module = fx.Module(
	"grpcclient",
	fx.Provide(newConn),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

func newConn(lc fx.Lifecycle, cfg Config, log logkit.Logger) (*grpc.ClientConn, error) {
	conn, err := Dial(cfg, log)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error { return conn.Close() },
	})
	return conn, nil
}
