package httpkit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит Config: caller регистрирует его сам
// (`fx.Provide(httpkit.NewConfig)` или из App-структуры).
var Module = fx.Module(
	"httpkit",
	fx.Provide(
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
	fx.Invoke(registerHealth),
)

func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

type idempotencyIn struct {
	fx.In
	Store IdempotencyStore `optional:"true"`
}

type AuthExtractorIn struct {
	fx.In
	Extract AuthExtractor `optional:"true"`
}

type healthIn struct {
	fx.In
	API    huma.API
	Log    logkit.Logger
	Probes []Probe `group:"health"`
}

type securitySchemesIn struct {
	fx.In
	Schemes []SecuritySchemeEntry `group:"httpkit.security_scheme"`
}

func registerHealth(in healthIn) {
	RegisterHealth(in.API, in.Log, in.Probes...)
}

// Порядок цепочки: RequestID и OTel первыми, чтобы request_id и trace_id были
// в ctx у всех остальных (включая access-log и panic-log).
func newServerWithLifecycle(lc fx.Lifecycle, cfg Config, log logkit.Logger, ai AuthExtractorIn, ii idempotencyIn, ss securitySchemesIn) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := NewServer(cfg, ss.Schemes...)

	mws := []func(http.Handler) http.Handler{
		RequestID(),
		OTel(),
		Recovery(log),
		SecurityHeaders(cfg.Security),
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
		AccessLog(log),
		Metrics(),
	}
	if ii.Store != nil {
		mws = append(mws, Idempotency(cfg.Idempotency, ii.Store))
	}
	s.router.Use(mws...)
	api := s.BindAPI()
	api.UseMiddleware(HumaAuth(api, ai.Extract))

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := s.Listen(); err != nil {
				return err
			}
			go func() { //nolint:gosec // горутина сервера живёт дольше ctx OnStart; фоновый ctx только для терминального лога
				if err := s.Serve(); err != nil {
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

	return s, nil
}
