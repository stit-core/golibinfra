// Package grpcclient — gRPC-клиент stit: *grpc.ClientConn к downstream-сервису
// с otel, access-логом, дедлайном по умолчанию, keepalive и bearer-токеном.
// Устройство и рецепты — README.md рядом с кодом.
package grpcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"git.stit.tech/stit-core/golibinfra/internal/grpclogkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Option настраивает Dial.
type Option func(*options)

type options struct {
	extra    []grpc.DialOption
	otelOpts []otelgrpc.Option
}

// WithDialOptions добавляет произвольные DialOption (load-balancing,
// service-config retry и т.п.) поверх дефолтных. Применяются последними,
// поэтому побеждают одноимённые опции кита.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.extra = append(o.extra, opts...) }
}

// WithStatsHandlerOptions настраивает otelgrpc-хендлер клиента
// (otelgrpc.WithFilter, otelgrpc.WithMessageEvents и т.п.).
func WithStatsHandlerOptions(opts ...otelgrpc.Option) Option {
	return func(o *options) { o.otelOpts = append(o.otelOpts, opts...) }
}

// Dial собирает *grpc.ClientConn к cfg.Target. Невалидный Config (ErrConfig)
// отвергается здесь, а не на первом вызове. grpc.NewClient ленивый —
// соединение устанавливается на первом RPC.
func Dial(cfg Config, log logkit.Logger, opts ...Option) (*grpc.ClientConn, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if log == nil {
		log = logkit.Nop()
	}
	grpclogkit.Install(log)

	o := options{}
	for _, fn := range opts {
		fn(&o)
	}

	creds, err := transportCredentials(cfg)
	if err != nil {
		return nil, err
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(o.otelOpts...)),
		grpc.WithChainUnaryInterceptor(requestIDUnary(), loggingUnary(log), timeoutUnary(cfg.Timeout)),
		grpc.WithChainStreamInterceptor(requestIDStream(), loggingStream(log)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cfg.MaxRecvMsgSize),
			grpc.MaxCallSendMsgSize(cfg.MaxSendMsgSize),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cfg.Keepalive.Time,
			Timeout:             cfg.Keepalive.Timeout,
			PermitWithoutStream: true,
		}),
	}
	if cfg.Token != "" {
		if !cfg.TLS {
			log.Warn(context.Background(), "grpcclient: bearer token over plaintext transport", "target", cfg.Target)
		}
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerPerRPC{token: cfg.Token, secure: cfg.TLS}))
	}
	dialOpts = append(dialOpts, o.extra...)

	return grpc.NewClient(cfg.Target, dialOpts...)
}

// transportCredentials собирает TLS-креды: свой CA — для внутренних PKI, чей
// корень не лежит в системном сторе; ServerName — когда target адресует под по
// IP, а сертификат выписан на имя сервиса.
func transportCredentials(cfg Config) (credentials.TransportCredentials, error) {
	if !cfg.TLS {
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.TLSServerName}
	if cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("grpcclient: read GRPC_CLIENT_TLS_CA_FILE %s: %w", cfg.TLSCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%w: GRPC_CLIENT_TLS_CA_FILE %s holds no PEM certificate", ErrConfig, cfg.TLSCAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return credentials.NewTLS(tlsCfg), nil
}
