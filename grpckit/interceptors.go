package grpckit

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// wrappedStream подменяет ctx у ServerStream (интерсепторы request-id/auth
// обогащают контекст, а ServerStream.Context() иначе read-only).
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context //nolint:containedctx // ctx стрима по контракту grpc
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func metadataFirst(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(key); len(v) > 0 { // md.Get кейс-инсенситивен
		return v[0]
	}
	return ""
}

// withRequestID кладёт x-request-id (или сгенерированный) под общий ключ httpkit,
// чтобы logkit-attr request_id коррелировал логи так же, как в HTTP.
func withRequestID(ctx context.Context) context.Context {
	id := metadataFirst(ctx, httpkit.HeaderRequestID)
	if id == "" {
		id = uuid.NewString()
	}
	return httpkit.ContextWithRequestID(ctx, id)
}

func requestIDUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(withRequestID(ctx), req)
	}
}

func requestIDStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: withRequestID(ss.Context())})
	}
}

func recoveryUnary(log logkit.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error(ctx, "grpc panic recovered", "method", info.FullMethod, "panic", rec, "stack", string(debug.Stack()))
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func recoveryStream(log logkit.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error(ss.Context(), "grpc panic recovered", "method", info.FullMethod, "panic", rec, "stack", string(debug.Stack()))
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

func loggingUnary(log logkit.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logRPC(ctx, log, info.FullMethod, time.Since(start), err)
		return resp, err
	}
}

func loggingStream(log logkit.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logRPC(ss.Context(), log, info.FullMethod, time.Since(start), err)
		return err
	}
}

func logRPC(ctx context.Context, log logkit.Logger, method string, dur time.Duration, err error) {
	code := status.Code(err)
	if err != nil {
		log.Error(ctx, "grpc call failed", "method", method, "code", code.String(), "duration_ms", dur.Milliseconds(), "error", err.Error())
		return
	}
	log.Debug(ctx, "grpc call", "method", method, "code", code.String(), "duration_ms", dur.Milliseconds())
}
