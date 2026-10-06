// Package httpkit — HTTP-сервер на huma/v2 + chi с готовой middleware-цепочкой
// (RequestID, ClientIP, OTel, AccessLog, Metrics, Recovery, SecurityHeaders, CORS,
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
	"sync/atomic"
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
	// ShutdownDrainDelay — пауза остановки, пока /ready отвечает 503, а listener ещё
	// принимает запросы: балансировщик успевает вывести инстанс.
	ShutdownDrainDelay time.Duration `envconfig:"HTTP_SHUTDOWN_DRAIN_DELAY" default:"0s"`
	// ShutdownTimeout — сколько после паузы дорабатывают запросы в полёте; на дедлайне их ctx
	// отменяется, а соединения закрываются.
	ShutdownTimeout time.Duration `envconfig:"HTTP_SHUTDOWN_TIMEOUT" default:"10s"`
	APITitle        string        `envconfig:"HTTP_API_TITLE" default:"API"`
	APIVersion      string        `envconfig:"HTTP_API_VERSION" default:"0.1.0"`
	// DocsEnabled=true отдаёт /docs, /openapi.* и /schemas без аутентификации — карту API.
	DocsEnabled bool `envconfig:"HTTP_DOCS_ENABLED" default:"false"`
	// ReadyProbeTimeout — потолок одной пробы /ready.
	ReadyProbeTimeout time.Duration `envconfig:"HTTP_READY_PROBE_TIMEOUT" default:"5s"`
	// QuietPaths — пути без Info-строки access-log и без server-спана.
	QuietPaths []string `envconfig:"HTTP_QUIET_PATHS" default:"/health,/ready"`
	// TrustProxyHops — сколько доверенных прокси перед сервисом дописывают X-Forwarded-For
	// (ClientIP): 0 — IP клиента — адрес соединения.
	TrustProxyHops int `envconfig:"HTTP_TRUST_PROXY_HOPS" default:"0"`
	CORS           CORSConfig
	RateLimit      RateLimitConfig
	MaxBody        MaxBodyConfig
	Security       SecurityHeadersConfig
	Idempotency    IdempotencyConfig
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
	case c.ShutdownDrainDelay < 0:
		return fmt.Errorf("%w: ShutdownDrainDelay must be >= 0", ErrConfig)
	case c.ReadyProbeTimeout <= 0:
		return fmt.Errorf("%w: ReadyProbeTimeout must be > 0", ErrConfig)
	case c.TrustProxyHops < 0:
		return fmt.Errorf("%w: TrustProxyHops must be >= 0", ErrConfig)
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

// ServerOption — опция NewServer: логгер, схемы безопасности, extractor, хранилище
// идемпотентности, middleware потребителя, правка huma.Config, публичные маршруты мимо huma.
// Опция с nil ничего не задаёт.
type ServerOption func(*serverOptions)

type serverOptions struct {
	log          logkit.Logger
	schemes      []SecuritySchemeEntry
	extract      AuthExtractor
	store        IdempotencyStore
	middleware   []func(http.Handler) http.Handler
	humaConfig   []func(*huma.Config)
	publicRoutes PublicRoutes
}

// WithLogger отдаёт серверу логгер: access-log, panic, ошибки net/http
// (`http.Server.ErrorLog`) и причины 5xx хендлеров уходят в него. Без опции логгер —
// logkit.Nop(): 5xx клиенту нейтральны всё равно, но причина теряется.
func WithLogger(log logkit.Logger) ServerOption {
	return func(o *serverOptions) {
		if log != nil {
			o.log = log
		}
	}
}

// WithSecuritySchemes добавляет схемы в `components.securitySchemes` OpenAPI.
func WithSecuritySchemes(schemes ...SecuritySchemeEntry) ServerOption {
	return func(o *serverOptions) { o.schemes = append(o.schemes, schemes...) }
}

// WithAuthExtractor отдаёт серверу AuthExtractor: HumaAuth проверяет им операции с Security,
// Idempotency делит ключи по субъекту. Без него операция с Security отвечает 500
// AUTH_NOT_CONFIGURED, а старт Module падает с ErrAuthNotConfigured.
func WithAuthExtractor(extract AuthExtractor) ServerOption {
	return func(o *serverOptions) {
		if extract != nil {
			o.extract = extract
		}
	}
}

// WithIdempotencyStore включает в цепочку кита Idempotency поверх store.
func WithIdempotencyStore(store IdempotencyStore) ServerOption {
	return func(o *serverOptions) {
		if store != nil {
			o.store = store
		}
	}
}

// WithMiddleware добавляет net/http-middleware потребителя внутрь цепочки кита в порядке
// перечисления (первый — внешний); nil пропускается.
func WithMiddleware(mw ...func(http.Handler) http.Handler) ServerOption {
	return func(o *serverOptions) {
		for _, m := range mw {
			if m != nil {
				o.middleware = append(o.middleware, m)
			}
		}
	}
}

// WithHumaConfig правит huma.Config перед созданием API: DocsRenderer,
// Servers, Transformers, дополнительные OnAddOperation. Правки идут после кита,
// но до хука описания ошибок, который видит их Security.
func WithHumaConfig(fn func(*huma.Config)) ServerOption {
	return func(o *serverOptions) {
		if fn != nil {
			o.humaConfig = append(o.humaConfig, fn)
		}
	}
}

// WithPublicRoutes объявляет открытыми маршруты chi мимо huma.Register (PublicRoutes).
func WithPublicRoutes(paths ...string) ServerOption {
	return func(o *serverOptions) { o.publicRoutes = append(o.publicRoutes, paths...) }
}

// Server — chi-роутер (транспорт) и huma.API (операции + OpenAPI) поверх
// одного *http.Server.
type Server struct {
	cfg  Config
	opts serverOptions

	router  *chi.Mux
	apiOnce sync.Once
	api     *registry
	srv     *http.Server
	// docsRoutes — маршруты /docs, /openapi.*, /schemas, которые humachi.New ставит мимо
	// huma.Register.
	docsRoutes map[string]bool

	// draining — Stop начался: /ready отвечает 503 (флаг едет в ctx запросов через baseCtx).
	draining atomic.Bool
	// baseCtx — родитель ctx всех запросов; Stop отменяет его на дедлайне остановки.
	baseCtx    context.Context
	cancelBase context.CancelFunc
	inflight   atomic.Int64

	mu sync.Mutex
	ln net.Listener
}

type drainingKey struct{}

// draining — запрос пришёл на сервер, который останавливается (Stop).
func draining(ctx context.Context) bool {
	flag, _ := ctx.Value(drainingKey{}).(*atomic.Bool)
	return flag != nil && flag.Load()
}

// NewServer собирает сервер кита — ту же сборку зовёт Module: цепочка middleware кита и
// middleware потребителя внутри неё регистрируются сразу, huma.API с HumaAuth — лениво в
// BindAPI. Невалидный cfg и схема безопасности без имени, без Scheme или дважды — ErrConfig.
func NewServer(cfg Config, opts ...ServerOption) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	o := serverOptions{log: logkit.Nop()}
	for _, opt := range opts {
		opt(&o)
	}
	if err := checkSchemes(o.schemes); err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	s := &Server{cfg: cfg, opts: o, router: r}
	s.baseCtx, s.cancelBase = context.WithCancel(context.WithValue(context.Background(), drainingKey{}, &s.draining))
	s.srv = &http.Server{
		Addr: cfg.Addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			s.inflight.Add(1)
			defer s.inflight.Add(-1)
			r.ServeHTTP(w, req)
		}),
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logkitHandler{log: o.log}, slog.LevelWarn),
	}
	r.Use(chain(cfg, o)...)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		writeAPIError(req.Context(), w, Error("NOT_FOUND", http.StatusNotFound, "route not found"))
	})
	r.MethodNotAllowed(methodNotAllowed(r))
	// huma.API создаётся лениво в BindAPI(): humachi.New регистрирует
	// /openapi.json и /docs, после чего chi запрещает router.Use(...).
	return s, nil
}

// chain — цепочка кита: RequestID, ClientIP и OTel первыми (request_id, IP клиента и trace_id
// в ctx у всех), AccessLog и Metrics — над Recovery, чтобы panic, 429 и preflight попадали в
// RED-метрики и access-log; middleware потребителя — внутри всей цепочки кита.
func chain(cfg Config, o serverOptions) []func(http.Handler) http.Handler {
	mws := []func(http.Handler) http.Handler{
		RequestID(),
		ClientIP(o.log, cfg.TrustProxyHops),
		OTel(cfg.QuietPaths...),
		AccessLog(o.log, cfg.QuietPaths...),
		Metrics(),
		Recovery(o.log),
		SecurityHeaders(cfg.Security),
		CORS(cfg.CORS),
		RateLimit(cfg.RateLimit),
		MaxBody(cfg.MaxBody),
	}
	if o.store != nil {
		var idem []IdempotencyOption
		if o.extract != nil {
			idem = append(idem, WithAuth(o.extract))
		}
		mws = append(mws, Idempotency(cfg.Idempotency, o.store, o.log, idem...))
	}
	return append(mws, o.middleware...)
}

// BindAPI инициализирует huma.API поверх chi с HumaAuth первым huma-middleware; операции,
// зарегистрированные на нём (и на его группах), проверяет CheckRoutes. Идемпотентен и
// безопасен из нескольких горутин; middleware chi после него не регистрируются.
func (s *Server) BindAPI() huma.API {
	s.apiOnce.Do(s.bindAPI)
	return s.api
}

func (s *Server) bindAPI() {
	humaConfig := huma.DefaultConfig(s.cfg.APITitle, s.cfg.APIVersion)
	// Единственный хук DefaultConfig — $schema в теле и Link в ответе: ссылка с хостом из
	// X-Forwarded-Host клиента, контракту ошибок не нужна, без docs ведёт в 404.
	humaConfig.CreateHooks = nil
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
	consumer := routeHandlers(s.router)
	api := humachi.New(s.router, humaConfig)
	s.docsRoutes = map[string]bool{}
	for route := range routeHandlers(s.router) {
		if _, ok := consumer[route]; !ok {
			s.docsRoutes[route] = true
		}
	}
	s.api = &registry{API: api}
	s.api.adapter = &opAdapter{Adapter: api.Adapter(), router: s.router, reg: s.api}
	s.api.UseMiddleware(HumaAuth(s.opts.log, s.opts.extract))
}

func (s *Server) registry() *registry {
	s.BindAPI()
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

// Stop останавливает сервер: /ready отвечает 503, через HTTP_SHUTDOWN_DRAIN_DELAY listener
// закрывается, а запросы в полёте дорабатывают ещё HTTP_SHUTDOWN_TIMEOUT, но не дольше
// дедлайна ctx. На дедлайне ctx хендлеров отменяется, соединения закрываются, число
// прерванных запросов — в WARN; ошибка — только у самого закрытия.
func (s *Server) Stop(ctx context.Context) error {
	s.draining.Store(true)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ShutdownDrainDelay+s.cfg.ShutdownTimeout)
	defer cancel()
	if s.cfg.ShutdownDrainDelay > 0 {
		pause := time.NewTimer(s.cfg.ShutdownDrainDelay)
		select {
		case <-pause.C:
		case <-ctx.Done():
		}
		pause.Stop()
	}
	err := s.srv.Shutdown(ctx)
	inflight := s.inflight.Load()
	s.cancelBase()
	if err == nil {
		return nil
	}
	if err := s.srv.Close(); err != nil {
		return fmt.Errorf("httpkit: close: %w", err)
	}
	s.opts.log.Warn(ctx, "http server stop deadline: requests cancelled, connections closed",
		"requests", inflight, "err", err)
	return nil
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
