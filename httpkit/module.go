package httpkit

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Module не provide'ит httpkit.Config — caller обязан зарегистрировать
// его сам. Это однородно с pgkit/telemetry: все три kit'а ждут свой
// Config извне (типично через App-структуру в app-config модуле).
// Если приложение не использует App-pattern — добавить
// `fx.Provide(httpkit.NewConfig)` рядом с httpkit.Module.
var Module = fx.Module(
	"httpkit",
	fx.Provide(
		newServerWithLifecycle,
		func(s *Server) huma.API { return s.API() },
	),
	fx.Invoke(registerHealth),
)

type idempotencyIn struct {
	fx.In
	Store IdempotencyStore `optional:"true"`
}

// meterIn — optional Meter. Если в DI есть telemetry.Module — Meter
// будет provide'ен; иначе пропустим Metrics middleware (otelhttp всё
// равно даст спаны через OTel() middleware).
type meterIn struct {
	fx.In
	Meter metric.Meter `optional:"true"`
}

// NewConfig — exported для приложений без App-pattern, которые хотят
// дешевле провайдить httpkit.Config из ENV: `fx.Provide(httpkit.NewConfig)`.
func NewConfig() (Config, error) {
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

type securitySchemesIn struct {
	fx.In
	Schemes []SecuritySchemeEntry `group:"httpkit.security_scheme"`
}

func registerHealth(in healthIn) {
	RegisterHealth(in.API, in.Probes...)
}

func newServerWithLifecycle(lc fx.Lifecycle, cfg Config, log logkit.Logger, ai AuthExtractorIn, ii idempotencyIn, ss securitySchemesIn, mi meterIn) *Server {
	s := NewServer(cfg, ss.Schemes...)

	mws := []func(http.Handler) http.Handler{
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
		RequestID(),
		Recovery(log),
		SecurityHeaders(cfg.Security),
		AccessLog(log),
	}
	if mi.Meter != nil {
		mws = append(mws, Metrics(mi.Meter))
	}
	mws = append(mws, OTel())
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
			go func() { //nolint:gosec // server goroutine outlives fx OnStart ctx; background ctx intentional for the terminal log
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
