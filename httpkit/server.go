// Package httpkit — HTTP-сервер на huma/v2 + chi с готовой
// middleware-цепочкой (CORS, RateLimit, MaxBody, RequestID, Recovery,
// SecurityHeaders, AccessLog, Metrics, OTel, опц. Idempotency) и
// унифицированной error-моделью APIError ({code,status,title,detail}).
// Эмитит метрики http_requests_total, http_request_duration_seconds,
// http_panics_total, http_max_body_rejections_total, http_idempotency_total.
package httpkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

type Config struct {
	Addr            string        `envconfig:"HTTP_ADDR" default:":8080"`
	ReadTimeout     time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"15s"`
	WriteTimeout    time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout     time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"60s"`
	ShutdownTimeout time.Duration `envconfig:"HTTP_SHUTDOWN_TIMEOUT" default:"15s"`
	APITitle        string        `envconfig:"HTTP_API_TITLE" default:"API"`
	APIVersion      string        `envconfig:"HTTP_API_VERSION" default:"0.1.0"`
	CORS            CORSConfig
	RateLimit       RateLimitConfig
	MaxBody         MaxBodyConfig
	Security        SecurityHeadersConfig
}

// SecuritySchemeEntry — одна запись в openapi `components.securitySchemes`.
// Регистрируется caller'ом через fx-группу `httpkit.security_scheme` или
// аргументом `NewServer`. httpkit не выставляет дефолтных схем.
type SecuritySchemeEntry struct {
	Name   string
	Scheme *huma.SecurityScheme
}

type Server struct {
	cfg     Config
	router  *chi.Mux
	api     huma.API
	srv     *http.Server
	schemes []SecuritySchemeEntry
}

func NewServer(cfg Config, schemes ...SecuritySchemeEntry) *Server {
	r := chi.NewRouter()
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	// huma.API создаётся лениво в BindAPI(): humachi.New на huma ≥ 2.37
	// сразу регистрирует /openapi.json и /docs route'ы, что блокирует
	// последующий router.Use(...) (chi panics "all middlewares must be
	// defined before routes"). Caller обязан вызвать BindAPI() ПОСЛЕ
	// router.Use(...).
	return &Server{cfg: cfg, router: r, srv: srv, schemes: schemes}
}

// BindAPI инициализирует huma.API поверх chi router'а. Идемпотентен.
// Вызывается caller'ом после того, как все middleware на router зарегистрированы.
func (s *Server) BindAPI() huma.API {
	if s.api != nil {
		return s.api
	}
	humaConfig := huma.DefaultConfig(s.cfg.APITitle, s.cfg.APIVersion)
	if len(s.schemes) > 0 {
		humaConfig.Components.SecuritySchemes = make(map[string]*huma.SecurityScheme, len(s.schemes))
		for _, e := range s.schemes {
			humaConfig.Components.SecuritySchemes[e.Name] = e.Scheme
		}
	}
	s.api = humachi.New(s.router, humaConfig)
	return s.api
}

func (s *Server) Router() *chi.Mux { return s.router }
func (s *Server) API() huma.API    { return s.BindAPI() }

func (s *Server) Start() error {
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpkit: serve: %w", err)
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
