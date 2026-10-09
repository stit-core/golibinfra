package grpckit

import (
	"context"
	"fmt"
	"net"
	"reflect"

	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/internal/stopbudget"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ServicesGroup — имя fx value-group, из которой Module собирает регистраторы
// доменных сервисов (регистрация — ProvideService).
const ServicesGroup = "grpckit.services"

// ProvideService регистрирует конструктор ServiceRegistrar (или []ServiceRegistrar — группа
// собирается через flatten) в группу ServicesGroup. Конструктор неверного типа отвергается при
// сборке графа: иначе сервис молча не навешивается на сервер. Дополнительные аннотации
// применяются до групповой; свой fx.ResultTags передавать нельзя.
func ProvideService(ctor any, anns ...fx.Annotation) fx.Option {
	tag, err := serviceTag(ctor)
	if err != nil {
		return fx.Error(err)
	}
	return fx.Provide(fx.Annotate(ctor, append(anns, fx.ResultTags(tag))...))
}

var (
	registrarType  = reflect.TypeFor[ServiceRegistrar]()
	registrarsType = reflect.TypeFor[[]ServiceRegistrar]()
)

func serviceTag(ctor any) (string, error) {
	t := reflect.TypeOf(ctor)
	if t == nil || t.Kind() != reflect.Func {
		return "", fmt.Errorf("grpckit: ProvideService expects a constructor function returning grpckit.ServiceRegistrar or []grpckit.ServiceRegistrar, got %T", ctor)
	}
	if t.NumOut() > 0 {
		switch t.Out(0) {
		case registrarType:
			return `group:"` + ServicesGroup + `"`, nil
		case registrarsType:
			return `group:"` + ServicesGroup + `,flatten"`, nil
		}
	}
	return "", fmt.Errorf("grpckit: ProvideService constructor must return grpckit.ServiceRegistrar or []grpckit.ServiceRegistrar as its first result, got %v", t)
}

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
			// Треть остатка бюджета остановки — хукам после gRPC: стрим в полёте её не съедает.
			ctx, cancel := stopbudget.Share(ctx)
			defer cancel()
			srv.Stop(ctx)
			return nil
		},
	})

	return srv, nil
}
