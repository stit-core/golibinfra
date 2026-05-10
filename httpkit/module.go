package httpkit

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit/golibinfra/logkit"
)

var Module = fx.Module("httpkit",
	fx.Provide(
		newConfig,
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
	fx.Invoke(registerHealth),
)

type idempotencyIn struct {
	fx.In
	Store IdempotencyStore `optional:"true"`
}

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

func newServerWithLifecycle(lc fx.Lifecycle, cfg Config, log logkit.Logger, ai AuthExtractorIn, ii idempotencyIn) *Server {
	s := NewServer(cfg)

	mws := []func(http.Handler) http.Handler{
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
		RequestID(),
		Recovery(log),
		SecurityHeaders(cfg.Security),
		AccessLog(log),
		OTel(),
	}
	if ii.Store != nil {
		mws = append(mws, Idempotency(IdempotencyConfig{
			Header:       "Idempotency-Key",
			TTL:          24 * time.Hour,
			MaxBodyBytes: 65536,
		}, ii.Store))
	}
	s.router.Use(mws...)
	// BindAPI ПОСЛЕ Use — humachi.New регистрирует /openapi.json /docs
	// route'ы, что блокирует chi.Mux.Use (panic "all middlewares must
	// be defined before routes on a mux").
	api := s.BindAPI()
	api.UseMiddleware(HumaAuth(api, ai.Extract))

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
