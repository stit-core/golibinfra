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
}

type Server struct {
	cfg    Config
	router *chi.Mux
	api    huma.API
	srv    *http.Server
}

func NewServer(cfg Config) *Server {
	r := chi.NewRouter()
	humaConfig := huma.DefaultConfig(cfg.APITitle, cfg.APIVersion)
	humaConfig.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"Bearer": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
		},
	}
	api := humachi.New(r, humaConfig)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	return &Server{cfg: cfg, router: r, api: api, srv: srv}
}

func (s *Server) Router() *chi.Mux { return s.router }
func (s *Server) API() huma.API    { return s.api }

func (s *Server) Start() error {
	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("httpkit: serve: %w", err)
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
