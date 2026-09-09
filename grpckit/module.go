package grpckit

import (
	"context"
	"fmt"
	"net"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ServicesGroup — имя fx value-group, из которой Module собирает регистраторы
// доменных сервисов:
//
//	fx.Provide(fx.Annotate(newMyServiceRegistrar, fx.ResultTags(`group:"grpckit.services"`)))
const ServicesGroup = "grpckit.services"

// Module отдаёт *Server и держит его lifecycle. fx.Invoke обязателен: без него
// конструктор сервера ленив, и приложение стартует с незанятым портом, если в
// графе никто не зависит от *Server.
//
// Config Module не provide'ит (asymmetry note в pgkit/module.go): дешёвый путь —
// fx.Provide(grpckit.NewConfig). Authenticator опционален: задан в графе —
// навешивается auth-интерсептор.
var Module = fx.Module(
	"grpckit",
	fx.Provide(newServer),
	fx.Invoke(func(*Server) {}),
)

type serverParams struct {
	fx.In

	LC            fx.Lifecycle
	Cfg           Config
	Log           logkit.Logger
	Authenticator Authenticator      `optional:"true"`
	Services      []ServiceRegistrar `group:"grpckit.services"`
	Probes        []healthkit.Probe  `group:"healthkit.probes"`
}

func newServer(p serverParams) (*Server, error) {
	opts := make([]ServerOption, 0, len(p.Services)+2)
	if p.Authenticator != nil {
		opts = append(opts, WithAuthenticator(p.Authenticator))
	}
	for _, reg := range p.Services {
		opts = append(opts, WithService(reg))
	}
	if len(p.Probes) > 0 {
		opts = append(opts, WithHealthProbes(p.Probes...))
	}

	srv, err := New(p.Cfg, p.Log, opts...)
	if err != nil {
		return nil, err
	}

	p.LC.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			var lc net.ListenConfig
			ln, err := lc.Listen(ctx, "tcp", srv.cfg.Addr)
			if err != nil {
				return fmt.Errorf("grpckit: listen %s: %w", srv.cfg.Addr, err)
			}
			go func() {
				p.Log.Info(ctx, "grpckit: serving", "addr", ln.Addr().String())
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
