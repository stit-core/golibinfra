package httpkit

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"go.uber.org/fx"
)

var Module = fx.Module("httpkit",
	fx.Provide(
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
)

// AuthExtractorIn — опциональная зависимость: если продукт зарегистрировал extractor, он подцепится.
type AuthExtractorIn struct {
	fx.In
	Extract AuthExtractor `optional:"true"`
}

func newServerWithLifecycle(lc fx.Lifecycle, cfg Config, log *slog.Logger, ai AuthExtractorIn) *Server {
	s := NewServer(cfg)

	s.router.Use(
		RequestID(),
		Recovery(log),
		AccessLog(log),
		OTel(),
	)
	if ai.Extract != nil {
		s.router.Use(Auth(ai.Extract))
	}

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := s.Start(); err != nil {
					log.Error("http server stopped", slog.Any("err", err))
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
