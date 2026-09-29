// Package httpkit — HTTP-сервер на huma/v2 + chi с готовой middleware-цепочкой
// (RequestID, OTel, AccessLog, Metrics, Recovery, SecurityHeaders, CORS,
// RateLimit, MaxBody, опц. Idempotency), health/readiness и контрактом ошибок
// APIError (problem+json). Эмитит http_requests_total, http_request_duration_seconds,
// http_panics_total, http_max_body_rejections_total, http_idempotency_total,
// http_rate_limited_total.
package httpkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Config — настройки сервера и вложенные конфиги middleware; читается
// NewConfig из ENV, валидируется Validate.
type Config struct {
	Addr              string        `envconfig:"HTTP_ADDR" default:":8080"`
	ReadTimeout       time.Duration `envconfig:"HTTP_READ_TIMEOUT" default:"15s"`
	ReadHeaderTimeout time.Duration `envconfig:"HTTP_READ_HEADER_TIMEOUT" default:"10s"`
	WriteTimeout      time.Duration `envconfig:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout       time.Duration `envconfig:"HTTP_IDLE_TIMEOUT" default:"60s"`
	ShutdownTimeout   time.Duration `envconfig:"HTTP_SHUTDOWN_TIMEOUT" default:"15s"`
	APITitle          string        `envconfig:"HTTP_API_TITLE" default:"API"`
	APIVersion        string        `envconfig:"HTTP_API_VERSION" default:"0.1.0"`
	// DocsEnabled=false убирает /docs, /openapi.* и /schemas с сервера.
	DocsEnabled bool `envconfig:"HTTP_DOCS_ENABLED" default:"true"`
	// ReadyProbeTimeout — потолок одной пробы /ready.
	ReadyProbeTimeout time.Duration `envconfig:"HTTP_READY_PROBE_TIMEOUT" default:"5s"`
	// QuietPaths — пути без Info-строки access-log и без server-спана.
	QuietPaths  []string `envconfig:"HTTP_QUIET_PATHS" default:"/health,/ready"`
	CORS        CORSConfig
	RateLimit   RateLimitConfig
	MaxBody     MaxBodyConfig
	Security    SecurityHeadersConfig
	Idempotency IdempotencyConfig
}

// ErrConfig — невалидный конфиг httpkit; оборачивается в ошибку старта.
var ErrConfig = errors.New("httpkit: invalid config")

// Validate проверяет конфиг целиком: сервер стартует только на пригодных
// значениях, а не падает на первом запросе.
func (c Config) Validate() error {
	switch {
	case c.Addr == "":
		return fmt.Errorf("%w: Addr is empty", ErrConfig)
	case c.ReadTimeout < 0 || c.ReadHeaderTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0:
		return fmt.Errorf("%w: timeouts must be >= 0", ErrConfig)
	case c.ShutdownTimeout <= 0:
		return fmt.Errorf("%w: ShutdownTimeout must be > 0", ErrConfig)
	case c.ReadyProbeTimeout <= 0:
		return fmt.Errorf("%w: ReadyProbeTimeout must be > 0", ErrConfig)
	case c.APITitle == "" || c.APIVersion == "":
		return fmt.Errorf("%w: APITitle and APIVersion are required", ErrConfig)
	case c.RateLimit.RPS < 0 || c.RateLimit.Burst < 0:
		return fmt.Errorf("%w: RateLimit.RPS and Burst must be >= 0", ErrConfig)
	case c.MaxBody.MaxBytes < 0:
		return fmt.Errorf("%w: MaxBody.MaxBytes must be >= 0", ErrConfig)
	case c.Idempotency.Header == "" || c.Idempotency.TTL <= 0 || c.Idempotency.ReserveTTL <= 0 || c.Idempotency.MaxBodyBytes <= 0:
		return fmt.Errorf("%w: Idempotency.Header, TTL, ReserveTTL and MaxBodyBytes are required", ErrConfig)
	case c.Idempotency.StoreTimeout <= 0:
		return fmt.Errorf("%w: Idempotency.StoreTimeout must be > 0", ErrConfig)
	case c.CORS.AllowCredentials && slices.Contains(c.CORS.Origins, "*"):
		return fmt.Errorf("%w: CORS.AllowCredentials is incompatible with origin \"*\"", ErrConfig)
	case c.CORS.MaxAge < 0:
		return fmt.Errorf("%w: CORS.MaxAge must be >= 0", ErrConfig)
	}
	return nil
}

// SecuritySchemeEntry — одна запись в openapi `components.securitySchemes`.
// Регистрируется caller'ом через fx-группу `httpkit.security_scheme` или
// опцией WithSecuritySchemes. httpkit не выставляет дефолтных схем.
type SecuritySchemeEntry struct {
	Name   string
	Scheme *huma.SecurityScheme
}

// ServerOption — опция NewServer: логгер, схемы безопасности, chi-middleware,
// правка huma.Config.
type ServerOption func(*serverOptions)

type serverOptions struct {
	log        logkit.Logger
	schemes    []SecuritySchemeEntry
	middleware []func(http.Handler) http.Handler
	humaConfig []func(*huma.Config)
}

// WithLogger отдаёт серверу логгер: ошибки net/http (`http.Server.ErrorLog`) и
// причины 5xx хендлеров уходят в него. Без опции логгер — logkit.Nop(): 5xx
// клиенту нейтральны всё равно, но причина теряется.
func WithLogger(log logkit.Logger) ServerOption {
	return func(o *serverOptions) { o.log = log }
}

// WithSecuritySchemes добавляет схемы в `components.securitySchemes` OpenAPI.
func WithSecuritySchemes(schemes ...SecuritySchemeEntry) ServerOption {
	return func(o *serverOptions) { o.schemes = append(o.schemes, schemes...) }
}

// WithMiddleware добавляет net/http-middleware в chi-цепочку в порядке
// перечисления; вызывается до BindAPI, поэтому chi принимает их все.
func WithMiddleware(mw ...func(http.Handler) http.Handler) ServerOption {
	return func(o *serverOptions) { o.middleware = append(o.middleware, mw...) }
}

// WithHumaConfig правит huma.Config перед созданием API: DocsRenderer,
// Servers, Transformers, дополнительные OnAddOperation. Правки идут после кита,
// но до хука описания ошибок, который видит их Security.
func WithHumaConfig(fn func(*huma.Config)) ServerOption {
	return func(o *serverOptions) { o.humaConfig = append(o.humaConfig, fn) }
}

// Server — chi-роутер (транспорт) и huma.API (операции + OpenAPI) поверх
// одного *http.Server.
type Server struct {
	cfg  Config
	opts serverOptions

	router *chi.Mux
	api    huma.API
	srv    *http.Server

	mu sync.Mutex
	ln net.Listener
}

// NewServer собирает сервер из конфига и опций; middleware из WithMiddleware
// регистрируются сразу, huma.API — лениво в BindAPI.
func NewServer(cfg Config, opts ...ServerOption) *Server {
	var o serverOptions
	for _, opt := range opts {
		opt(&o)
	}
	r := chi.NewRouter()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	if o.log == nil {
		o.log = logkit.Nop()
	}
	srv.ErrorLog = slog.NewLogLogger(logkitHandler{log: o.log}, slog.LevelWarn)
	if len(o.middleware) > 0 {
		r.Use(o.middleware...)
	}
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeAPIError(req.Context(), w, Error("NOT_FOUND", http.StatusNotFound, "route not found"))
	})
	r.MethodNotAllowed(methodNotAllowed(r))
	// huma.API создаётся лениво в BindAPI(): humachi.New регистрирует
	// /openapi.json и /docs, после чего chi запрещает router.Use(...).
	return &Server{cfg: cfg, opts: o, router: r, srv: srv}
}

// BindAPI инициализирует huma.API поверх chi. Идемпотентен; вызывать после
// того, как все middleware зарегистрированы.
func (s *Server) BindAPI() huma.API {
	if s.api != nil {
		return s.api
	}
	humaConfig := huma.DefaultConfig(s.cfg.APITitle, s.cfg.APIVersion)
	if !s.cfg.DocsEnabled {
		humaConfig.DocsPath, humaConfig.OpenAPIPath, humaConfig.SchemasPath = "", "", ""
	}
	if len(s.opts.schemes) > 0 {
		humaConfig.Components.SecuritySchemes = make(map[string]*huma.SecurityScheme, len(s.opts.schemes))
		for _, e := range s.opts.schemes {
			humaConfig.Components.SecuritySchemes[e.Name] = e.Scheme
		}
	}
	maxBody := s.cfg.MaxBody.MaxBytes
	humaConfig.OnAddOperation = append(humaConfig.OnAddOperation,
		func(_ *huma.OpenAPI, op *huma.Operation) { op.MaxBodyBytes = maxBody })
	humaConfig.Transformers = append(humaConfig.Transformers, s.transformError)
	for _, fn := range s.opts.humaConfig {
		fn(&humaConfig)
	}
	// Описание ошибок — после хуков потребителя: их Security тоже объявляет 401.
	humaConfig.OnAddOperation = append(humaConfig.OnAddOperation, describeErrors)
	s.api = humachi.New(s.router, humaConfig)
	return s.api
}

// Router — chi-роутер сервера; middleware регистрируются только до BindAPI.
func (s *Server) Router() *chi.Mux { return s.router }

// API — huma.API сервера (алиас BindAPI).
func (s *Server) API() huma.API { return s.BindAPI() }

// Listen занимает порт синхронно: занятый адрес — ошибка старта, а не строка в логе.
func (s *Server) Listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", s.cfg.Addr)
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

// Stop останавливает приём соединений и ждёт завершения текущих до ctx.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// transformError дополняет ответ-ошибку huma перед записью статуса: request_id
// запроса, Retry-After и строка handler error с причиной 5xx в логе сервера.
func (s *Server) transformError(hc huma.Context, _ string, v any) (any, error) {
	e, ok := v.(*APIError)
	if !ok {
		return v, nil
	}
	ctx := hc.Context()
	if e.Status >= http.StatusInternalServerError {
		kv := []any{"status", e.Status, "code", e.Code}
		if op := hc.Operation(); op != nil {
			kv = append(kv, "operation", op.OperationID)
		}
		if e.cause != nil {
			kv = append(kv, "err", e.cause)
		}
		s.opts.log.Error(ctx, "handler error", kv...)
	}
	// Retry-After, выставленный хендлером (huma.ErrorWithHeaders), точнее вычисленного.
	if ra := e.retryAfter(); ra != "" && responseHeader(hc).Get("Retry-After") == "" {
		hc.SetHeader("Retry-After", ra)
	}
	return e.forRequest(ctx), nil
}

// responseHeader — заголовки ответа адаптера; nil, если writer недоступен.
func responseHeader(hc huma.Context) http.Header {
	if _, w := unwrapHTTP(hc); w != nil {
		return w.Header()
	}
	return nil
}

// methodNotAllowed — 405 маршрутизатора с problem+json и Allow: свой обработчик
// chi не выставляет Allow сам, поэтому методы маршрута ищутся заново.
func methodNotAllowed(router chi.Routes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.RawPath
		if path == "" {
			path = r.URL.Path
		}
		for _, m := range knownMethods {
			if router.Match(chi.NewRouteContext(), m, path) {
				w.Header().Add("Allow", m)
			}
		}
		writeAPIError(r.Context(), w, Error("METHOD_NOT_ALLOWED", http.StatusMethodNotAllowed, "method not allowed"))
	}
}

// logkitHandler — мост stdlib log → logkit: net/http пишет ошибки соединений
// через *log.Logger, а в сервисах допустим единственный sink logkit.
type logkitHandler struct{ log logkit.Logger }

func (h logkitHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.log.Enabled(ctx, l)
}

func (h logkitHandler) Handle(ctx context.Context, r slog.Record) error {
	h.log.Warn(ctx, "http server error", "err", r.Message)
	return nil
}

func (h logkitHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h logkitHandler) WithGroup(string) slog.Handler { return h }
