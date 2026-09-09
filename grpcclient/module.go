package grpcclient

import (
	"context"

	"go.uber.org/fx"
	"google.golang.org/grpc"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module провайдит один *grpc.ClientConn к GRPC_CLIENT_TARGET и закрывает его
// на остановке приложения; для нескольких таргетов — Dial напрямую.
//
// Config Module не provide'ит (asymmetry note в pgkit/module.go): дешёвый путь —
// fx.Provide(grpcclient.NewConfig).
var Module = fx.Module(
	"grpcclient",
	fx.Provide(newConn),
)

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
