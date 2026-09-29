package httpkit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/kelseyhightower/envconfig"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// SecuritySchemeGroup — fx-группа схем безопасности OpenAPI (SecuritySchemeEntry).
const SecuritySchemeGroup = "httpkit.security_scheme"

// MiddlewareGroup — fx-группа middleware потребителя; они идут внутри цепочки кита.
const MiddlewareGroup = "httpkit.middleware"

// HumaConfigGroup — fx-группа правок huma.Config перед созданием API.
const HumaConfigGroup = "httpkit.huma_config"

// Module — сервер, huma.API и /health + /ready; Config регистрирует caller
// (`fx.Provide(httpkit.NewConfig)` или из App-структуры). HTTP слушает после
// OnStart всех fx.Module графа и гасится до их OnStop: хук сервера вешает Invoke
// уровня Module, а fx выполняет его после Invoke всех fx.Module. Поэтому Module —
// на верхнем уровне fx.New, а корневой fx.Invoke, который первым строит
// зависимость с хуками, — до него.
var Module = fx.Options(
	fx.Module(
		"httpkit",
		fx.Provide(
			newServer,
			func(s *Server) huma.API { return s.API() },
		),
		fx.Invoke(fx.Annotate(
			registerHealth,
			fx.ParamTags(``, ``, ``, `group:"`+healthkit.Group+`"`),
		)),
	),
	fx.Invoke(serve),
)

// NewConfig читает HTTP_* из ENV и валидирует конфиг: невалидный — ошибка старта.
func NewConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

type serverIn struct {
	fx.In

	Config Config
	Log    logkit.Logger

	Extract    AuthExtractor                     `optional:"true"`
	Store      IdempotencyStore                  `optional:"true"`
	Schemes    []SecuritySchemeEntry             `group:"httpkit.security_scheme"`
	Middleware []func(http.Handler) http.Handler `group:"httpkit.middleware"`
	HumaConfig []func(*huma.Config)              `group:"httpkit.huma_config"`
}

func registerHealth(api huma.API, cfg Config, log logkit.Logger, probes []Probe) {
	RegisterHealth(api, log, cfg.ReadyProbeTimeout, probes...)
}

// Порядок цепочки: RequestID и OTel первыми (request_id и trace_id в ctx у
// всех), AccessLog и Metrics — над Recovery, чтобы panic, 429 и preflight
// попадали в RED-метрики и access-log; middleware потребителя — внутри всей
// цепочки кита.
func newServer(in serverIn) (*Server, error) {
	cfg := in.Config
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	mws := []func(http.Handler) http.Handler{
		RequestID(),
		OTel(cfg.QuietPaths...),
		AccessLog(in.Log, cfg.QuietPaths...),
		Metrics(),
		Recovery(in.Log),
		SecurityHeaders(cfg.Security),
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
	}
	if in.Store != nil {
		var opts []IdempotencyOption
		if in.Extract != nil {
			opts = append(opts, WithAuth(in.Extract))
		}
		mws = append(mws, Idempotency(cfg.Idempotency, in.Store, opts...))
	}
	mws = append(mws, in.Middleware...)

	opts := []ServerOption{
		WithLogger(in.Log),
		WithSecuritySchemes(in.Schemes...),
		WithMiddleware(mws...),
	}
	for _, fn := range in.HumaConfig {
		opts = append(opts, WithHumaConfig(fn))
	}
	s := NewServer(cfg, opts...)
	s.BindAPI().UseMiddleware(HumaAuth(in.Log, in.Extract))
	return s, nil
}

// serve вешает хук сервера. Это Invoke уровня Module, а не конструктор: хуки идут
// в порядке добавления, и сервер обязан быть последним, чей хук добавлен.
func serve(lc fx.Lifecycle, sd fx.Shutdowner, s *Server) {
	log := s.opts.log
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := s.Listen(); err != nil {
				return err
			}
			log.Info(ctx, "http server listening", "addr", s.Addr().String())
			go func() { //nolint:gosec // горутина сервера живёт дольше ctx OnStart; фоновый ctx только для терминального лога
				if err := s.Serve(); err != nil {
					log.Error(context.Background(), "http server stopped", "err", err)
					// Упавший listener — не «работающий» сервис: fx должен погасить приложение.
					_ = sd.Shutdown(fx.ExitCode(1))
				}
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			ctx, cancel := context.WithTimeout(stopCtx, s.cfg.ShutdownTimeout)
			defer cancel()
			return s.Stop(ctx)
		},
	})
}
