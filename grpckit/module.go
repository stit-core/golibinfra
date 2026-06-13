package grpckit

import (
	"context"
	"fmt"
	"net"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config (asymmetry note в pgkit/module.go). Дешёвый mode:
// fx.Provide(grpckit.NewConfig). Сервисы — во fx value-group "grpc.services":
//
//	fx.Provide(fx.Annotate(newMyServiceRegistrar, fx.ResultTags(`group:"grpc.services"`)))
//
// Authenticator опционален: задан — навешивается auth-интерсептор.
var Module = fx.Module(
	"grpckit",
	fx.Provide(newServer),
)

func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

type serverParams struct {
	fx.In

	LC            fx.Lifecycle
	Cfg           Config
	Log           logkit.Logger
	Authenticator Authenticator      `optional:"true"`
	Services      []ServiceRegistrar `group:"grpc.services"`
}

func newServer(p serverParams) (*Server, error) {
	opts := make([]ServerOption, 0, len(p.Services)+1)
	if p.Authenticator != nil {
		opts = append(opts, WithAuthenticator(p.Authenticator))
	}
	for _, reg := range p.Services {
		opts = append(opts, WithService(reg))
	}

	srv, err := New(p.Cfg, p.Log, opts...)
	if err != nil {
		return nil, err
	}

	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", p.Cfg.Addr)
			if err != nil {
				return fmt.Errorf("grpckit: listen %s: %w", p.Cfg.Addr, err)
			}
			go func() {
				p.Log.Info(ctx, "grpckit: serving", "addr", p.Cfg.Addr)
				if err := srv.Serve(ln); err != nil {
					p.Log.Error(ctx, "grpckit: serve stopped", "error", err.Error())
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			srv.Stop(ctx)
			return nil
		},
	})

	return srv, nil
}
