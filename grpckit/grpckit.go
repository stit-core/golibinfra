// Package grpckit — серверный gRPC-примитив (аналог httpkit для gRPC):
// *grpc.Server с цепочкой интерсепторов (recovery, request-id, access-log,
// опц. auth через порт Authenticator), otel через StatsHandler, health- и
// (опц.) reflection-сервисы. Сервисы регистрируются через fx value-group
// "grpc.services". Метрики/спаны эмитит otelgrpc (rpc.server.*). Слушает свой
// порт (GRPC_ADDR), HTTP/2.
package grpckit

import (
	"context"
	"fmt"
	"net"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ServiceRegistrar регистрирует gRPC-сервис на сервере. Потребитель отдаёт свои
// gen-стабы во fx value-group "grpc.services"; grpckit применяет их до Serve.
type ServiceRegistrar func(*grpc.Server)

type serverOptions struct {
	auth     Authenticator
	services []ServiceRegistrar
}

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

type Server struct {
	cfg    Config
	log    logkit.Logger
	gsrv   *grpc.Server
	health *health.Server
}

// New собирает сервер (интерсепторы, health, опц. reflection, сервисы), но не
// слушает порт. Старт/стоп — через Serve/Stop (или fx-модуль с lifecycle).
func New(cfg Config, log logkit.Logger, opts ...ServerOption) (*Server, error) {
	if log == nil {
		log = logkit.Nop()
	}
	o := serverOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	gopts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.MaxRecvMsgSize(cfg.MaxRecvMiB << 20),
		grpc.MaxSendMsgSize(cfg.MaxSendMiB << 20),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.Keepalive.Time,
			Timeout: cfg.Keepalive.Timeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             cfg.Keepalive.MinTime,
			PermitWithoutStream: true,
		}),
	}
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		creds, err := credentials.NewServerTLSFromFile(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("grpckit: tls creds: %w", err)
		}
		gopts = append(gopts, grpc.Creds(creds))
	}

	unary := []grpc.UnaryServerInterceptor{recoveryUnary(log), requestIDUnary(), loggingUnary(log)}
	stream := []grpc.StreamServerInterceptor{recoveryStream(log), requestIDStream(), loggingStream(log)}
	if o.auth != nil {
		unary = append(unary, authUnary(o.auth))
		stream = append(stream, authStream(o.auth))
	}
	gopts = append(gopts, grpc.ChainUnaryInterceptor(unary...), grpc.ChainStreamInterceptor(stream...))

	gsrv := grpc.NewServer(gopts...)
	hsrv := health.NewServer()
	healthpb.RegisterHealthServer(gsrv, hsrv)
	for _, reg := range o.services {
		reg(gsrv)
	}
	if cfg.Reflection {
		reflection.Register(gsrv)
	}

	return &Server{cfg: cfg, log: log, gsrv: gsrv, health: hsrv}, nil
}

// GRPC — нижележащий *grpc.Server (поздняя регистрация сервисов, тесты).
func (s *Server) GRPC() *grpc.Server { return s.gsrv }

// SetServing управляет health-статусом сервиса ("" — overall).
func (s *Server) SetServing(service string, ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus(service, st)
}

// Serve блокирующе обслуживает соединения на ln (overall health → SERVING).
func (s *Server) Serve(ln net.Listener) error {
	s.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	return s.gsrv.Serve(ln)
}

// Stop — graceful-остановка в пределах ShutdownGrace, иначе форс.
func (s *Server) Stop(ctx context.Context) {
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
