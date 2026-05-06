package httpkit

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/akadmin/golibinfra/logkit"
)

var Module = fx.Module("httpkit",
	fx.Provide(
		newConfig,
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
	fx.Invoke(registerHealth),
)

func newConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}

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
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
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
			ctx, cancel := context.WithTimeout(stopCtx, cfg.ShutdownTimeout)
			defer cancel()
			return s.Stop(ctx)
		},
	})

	return s
}
