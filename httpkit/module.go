package httpkit

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"

	"git.stit.tech/akadmin/golibinfra/logkit"
)

var Module = fx.Module("httpkit",
	fx.Provide(
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
	fx.Invoke(registerHealth),
)

type AuthExtractorIn struct {
	fx.In
	Extract AuthExtractor `optional:"true"`
}

type healthIn struct {
	fx.In
	API    huma.API
	Probes []Probe `group:"health"`
}

func registerHealth(in healthIn) {
	RegisterHealth(in.API, in.Probes...)
}

func newServerWithLifecycle(lc fx.Lifecycle, cfg Config, log logkit.Logger, ai AuthExtractorIn) *Server {
	s := NewServer(cfg)

	s.router.Use(
		RequestID(),
		Recovery(log),
		AccessLog(log),
		OTel(),
	)
	s.api.UseMiddleware(HumaAuth(s.api, ai.Extract))

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := s.Start(); err != nil {
					log.Error(context.Background(), "http server stopped", "err", err)
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			return s.Stop(stopCtx)
		},
	})

	return s
}
