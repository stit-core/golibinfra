package grpcclient

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

func loggingUnary(log logkit.Logger) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		code := status.Code(err)
		kv := []any{"method", method, "code", code.String(), "duration_ms", time.Since(start).Milliseconds()}
		switch {
		case err == nil:
			log.Debug(ctx, "grpc client call", kv...)
		case isServerFault(code):
			log.Error(ctx, "grpc client call failed", append(kv, "error", err.Error())...)
		default:
			log.Warn(ctx, "grpc client call failed", append(kv, "error", err.Error())...)
		}
		return err
	}
}

// isServerFault — коды сбоя downstream'а или транспорта; остальные не-OK исходы
// (ошибка запроса, отказ в доступе, отмена) — ожидаемые, для них Warn.
func isServerFault(code codes.Code) bool {
	switch code {
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.Unimplemented:
		return true
	}
	return false
}
