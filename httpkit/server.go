// Package httpkit — HTTP-сервер на huma/v2 + chi с готовой middleware-цепочкой
// (RequestID, OTel, Recovery, SecurityHeaders, CORS, RateLimit, MaxBody,
// AccessLog, Metrics, опц. Idempotency), health/readiness и error-моделью
// APIError. Эмитит http_requests_total, http_request_duration_seconds,
// http_panics_total, http_max_body_rejections_total, http_idempotency_total,
// http_rate_limited_total.
package httpkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

type Config struct {
	Addr              string        `envconfig:"HTTP_ADDR" default:":8080"`
	ReadTimeout       time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"15s"`
	ReadHeaderTimeout time.Duration `envconfig:"HTTP_READ_HEADER_TIMEOUT" default:"10s"`
	WriteTimeout      time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout       time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"60s"`
	ShutdownTimeout   time.Duration `envconfig:"HTTP_SHUTDOWN_TIMEOUT" default:"15s"`
	APITitle          string        `envconfig:"HTTP_API_TITLE" default:"API"`
	APIVersion        string        `envconfig:"HTTP_API_VERSION" default:"0.1.0"`
	CORS              CORSConfig
	RateLimit         RateLimitConfig
	MaxBody           MaxBodyConfig
	Security          SecurityHeadersConfig
	Idempotency       IdempotencyConfig
}

var ErrConfig = errors.New("httpkit: invalid config")

func (c Config) Validate() error {
	switch {
	case c.Addr == "":
		return fmt.Errorf("%w: Addr is empty", ErrConfig)
	case c.ReadTimeout < 0 || c.ReadHeaderTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0:
		return fmt.Errorf("%w: timeouts must be >= 0", ErrConfig)
	case c.ShutdownTimeout <= 0:
		return fmt.Errorf("%w: ShutdownTimeout must be > 0", ErrConfig)
	case c.RateLimit.RPS < 0 || c.RateLimit.Burst < 0:
		return fmt.Errorf("%w: RateLimit.RPS and Burst must be >= 0", ErrConfig)
	case c.MaxBody.MaxBytes < 0:
		return fmt.Errorf("%w: MaxBody.MaxBytes must be >= 0", ErrConfig)
	case c.Idempotency.Header == "" || c.Idempotency.TTL <= 0 || c.Idempotency.ReserveTTL <= 0 || c.Idempotency.MaxBodyBytes <= 0:
		return fmt.Errorf("%w: Idempotency.Header, TTL, ReserveTTL and MaxBodyBytes are required", ErrConfig)
	case c.CORS.AllowCredentials && containsStar(c.CORS.Origins):
		return fmt.Errorf("%w: CORS.AllowCredentials is incompatible with origin \"*\"", ErrConfig)
	}
	return nil
}

func containsStar(origins []string) bool {
	for _, o := range origins {
		if o == "*" {
			return true
		}
	}
	return false
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

	mu sync.Mutex
	ln net.Listener
}

func NewServer(cfg Config, schemes ...SecuritySchemeEntry) *Server {
	r := chi.NewRouter()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	// huma.API создаётся лениво в BindAPI(): humachi.New регистрирует
	// /openapi.json и /docs, после чего chi запрещает router.Use(...).
	return &Server{cfg: cfg, router: r, srv: srv, schemes: schemes}
}

// BindAPI инициализирует huma.API поверх chi. Идемпотентен; вызывать после
// того, как все middleware зарегистрированы.
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

// Listen занимает порт синхронно: занятый адрес — ошибка старта, а не строка в логе.
func (s *Server) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("httpkit: listen %s: %w", s.cfg.Addr, err)
	}
	s.ln = ln
	return nil
}

// Addr — фактический адрес после Listen (полезно при HTTP_ADDR=:0); nil до Listen.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve обслуживает соединения до Stop; при необходимости вызывает Listen.
func (s *Server) Serve() error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpkit: serve: %w", err)
	}
	return nil
}

// Start = Serve; для ручной проводки без fx.
func (s *Server) Start() error { return s.Serve() }

func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
