package grpcclient

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func loggingUnary(log logkit.Logger) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		dur := time.Since(start)
		if err != nil {
			log.Error(ctx, "grpc client call failed", "method", method, "code", status.Code(err).String(), "duration_ms", dur.Milliseconds(), "error", err.Error())
		} else {
			log.Debug(ctx, "grpc client call", "method", method, "duration_ms", dur.Milliseconds())
		}
		return err
	}
}
