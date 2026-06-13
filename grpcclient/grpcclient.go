// Package grpcclient — клиентский gRPC-примитив (аналог httpclient для gRPC):
// *grpc.ClientConn с otel (StatsHandler → rpc.client.*), access-log, keepalive и
// PerRPCCredentials — инъекцией bearer-токена (из env) в authorization на каждый
// вызов. Сгенерированный типизированный клиент потребитель строит поверх Conn.
package grpcclient

import (
	"crypto/tls"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

type Option func(*options)

type options struct {
	extra []grpc.DialOption
}

// WithDialOptions добавляет произвольные DialOption (load-balancing,
// service-config retry и т.п.) поверх дефолтных.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.extra = append(o.extra, opts...) }
}

// Dial собирает *grpc.ClientConn к cfg.Target. grpc.NewClient ленивый —
// соединение устанавливается на первом вызове.
func Dial(cfg Config, log logkit.Logger, opts ...Option) (*grpc.ClientConn, error) {
	if log == nil {
		log = logkit.Nop()
	}
	o := options{}
	for _, fn := range opts {
		fn(&o)
	}

	dialOpts := []grpc.DialOption{
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(loggingUnary(log)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cfg.Keepalive.Time,
			Timeout:             cfg.Keepalive.Timeout,
			PermitWithoutStream: true,
		}),
	}
	if cfg.TLS {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if cfg.Token != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerPerRPC{token: cfg.Token, secure: cfg.TLS}))
	}
	dialOpts = append(dialOpts, o.extra...)

	return grpc.NewClient(cfg.Target, dialOpts...)
}
