// Package grpckit — gRPC-сервер stit: интерсепторы, observability, health и
// lifecycle транспорта; доменные сервисы подключаются данными через fx-группу
// ServicesGroup. Устройство и рецепты — README.md и docs/ рядом с кодом.
package grpckit

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/internal/grpclogkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ServiceRegistrar регистрирует gRPC-сервис на сервере. Потребитель отдаёт свои
// gen-стабы во fx value-group ServicesGroup; grpckit применяет их до Serve.
type ServiceRegistrar func(*grpc.Server)

type serverOptions struct {
	auth       Authenticator
	services   []ServiceRegistrar
	probes     []healthkit.Probe
	unary      []grpc.UnaryServerInterceptor
	stream     []grpc.StreamServerInterceptor
	serverOpts []grpc.ServerOption
	otelOpts   []otelgrpc.Option
}

// ServerOption настраивает New вне fx-графа.
type ServerOption func(*serverOptions)

// WithAuthenticator включает auth-интерсептор (порт реализует потребитель,
// напр. access-домен). Без него auth не навешивается (всё открыто).
func WithAuthenticator(a Authenticator) ServerOption {
	return func(o *serverOptions) { o.auth = a }
}

// WithService регистрирует сервис вне fx (тесты, не-fx сборка).
func WithService(r ServiceRegistrar) ServerOption {
	return func(o *serverOptions) { o.services = append(o.services, r) }
}

// WithHealthProbes добавляет пробы, по которым сервер каждые
// Config.HealthInterval переключает overall-статус gRPC health.
func WithHealthProbes(p ...healthkit.Probe) ServerOption {
	return func(o *serverOptions) { o.probes = append(o.probes, p...) }
}

// WithUnaryInterceptors добавляет unary-интерсепторы в хвост цепочки кита —
// после auth, вплотную к handler'у.
func WithUnaryInterceptors(i ...grpc.UnaryServerInterceptor) ServerOption {
	return func(o *serverOptions) { o.unary = append(o.unary, i...) }
}

// WithStreamInterceptors добавляет stream-интерсепторы в хвост цепочки кита —
// после auth, вплотную к handler'у.
func WithStreamInterceptors(i ...grpc.StreamServerInterceptor) ServerOption {
	return func(o *serverOptions) { o.stream = append(o.stream, i...) }
}

// WithServerOptions добавляет произвольные grpc.ServerOption (mTLS через
// grpc.Creds, свой codec, ConnectionTimeout). Применяются последними, поэтому
// побеждают одноимённые опции кита.
func WithServerOptions(opts ...grpc.ServerOption) ServerOption {
	return func(o *serverOptions) { o.serverOpts = append(o.serverOpts, opts...) }
}

// WithStatsHandlerOptions настраивает otelgrpc-хендлер сервера
// (otelgrpc.WithFilter, otelgrpc.WithMessageEvents и т.п.).
func WithStatsHandlerOptions(opts ...otelgrpc.Option) ServerOption {
	return func(o *serverOptions) { o.otelOpts = append(o.otelOpts, opts...) }
}

// Server — gRPC-сервер кита: транспорт, health-сервис и их lifecycle.
type Server struct {
	cfg    Config
	log    logkit.Logger
	gsrv   *grpc.Server
	health *health.Server
	probes []healthkit.Probe

	mu         sync.Mutex
	addr       string
	stopped    bool
	healthStop context.CancelFunc
	healthDone chan struct{}
}

// New собирает сервер (интерсепторы, health, опц. reflection, сервисы), но не
// слушает порт. Старт/стоп — через Serve/Stop (или fx-модуль с lifecycle).
// Невалидный Config (ErrConfig) отвергается здесь, а не на первом запросе.
func New(cfg Config, log logkit.Logger, opts ...ServerOption) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if log == nil {
		log = logkit.Nop()
	}
	grpclogkit.Install(log)

	o := serverOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	gopts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler(o.otelOpts...)),
		grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.MaxSendMsgSize),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.Keepalive.Time,
			Timeout: cfg.Keepalive.Timeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             cfg.Keepalive.MinTime,
			PermitWithoutStream: true,
		}),
	}
	if cfg.MaxConcurrentStreams > 0 {
		gopts = append(gopts, grpc.MaxConcurrentStreams(cfg.MaxConcurrentStreams))
	}
	if cfg.TLSCertFile != "" {
		creds, err := credentials.NewServerTLSFromFile(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("grpckit: tls creds: %w", err)
		}
		gopts = append(gopts, grpc.Creds(creds))
	}

	unary := []grpc.UnaryServerInterceptor{requestIDUnary(), loggingUnary(log), recoveryUnary(log)}
	stream := []grpc.StreamServerInterceptor{requestIDStream(), loggingStream(log), recoveryStream(log)}
	if o.auth != nil {
		unary = append(unary, authUnary(o.auth, log))
		stream = append(stream, authStream(o.auth, log))
	}
	unary = append(unary, o.unary...)
	stream = append(stream, o.stream...)
	gopts = append(gopts, grpc.ChainUnaryInterceptor(unary...), grpc.ChainStreamInterceptor(stream...))
	gopts = append(gopts, o.serverOpts...)

	gsrv := grpc.NewServer(gopts...)
	hsrv := health.NewServer()
	healthpb.RegisterHealthServer(gsrv, hsrv)
	for _, reg := range o.services {
		reg(gsrv)
	}
	if cfg.Reflection {
		reflection.Register(gsrv)
	}

	return &Server{cfg: cfg, log: log, gsrv: gsrv, health: hsrv, probes: o.probes}, nil
}

// GRPC возвращает нижележащий *grpc.Server (поздняя регистрация сервисов,
// интероп).
func (s *Server) GRPC() *grpc.Server { return s.gsrv }

// SetServing управляет health-статусом сервиса ("" — overall).
func (s *Server) SetServing(service string, ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus(service, st)
}

// Addr возвращает фактический адрес слушателя ("" — Serve ещё не вызывался).
// Он отличается от Config.Addr, когда порт выбирает ядро (":0" в тестах).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Serve блокирующе обслуживает соединения на ln: overall health → SERVING, а
// при заданных пробах и HealthInterval — периодическая переоценка статуса.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.addr = ln.Addr().String()
	s.mu.Unlock()

	s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	s.startHealthWatch()
	return s.gsrv.Serve(ln)
}

// Stop останавливает сервер: health → NOT_SERVING (оркестратор уводит трафик),
// затем graceful в пределах ShutdownGrace, по таймауту или отмене ctx — форс.
func (s *Server) Stop(ctx context.Context) {
	s.stopHealthWatch()
	s.health.Shutdown()

	done := make(chan struct{})
	go func() {
		s.gsrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.cfg.ShutdownGrace):
		s.gsrv.Stop()
	case <-ctx.Done():
		s.gsrv.Stop()
	}
}
