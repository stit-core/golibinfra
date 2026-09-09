package grpcclient

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/internal/grpcstatus"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// withOutgoingRequestID продолжает цепочку корреляции: id входящего запроса
// (его положил httpkit или grpckit) уезжает в downstream тем же metadata-ключом,
// который downstream ждёт. Заданный вручную ключ не перетирается.
func withOutgoingRequestID(ctx context.Context) context.Context {
	id := requestid.From(ctx)
	if id == "" {
		return ctx
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(requestid.Header)) > 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, requestid.Header, id)
}

func requestIDUnary() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withOutgoingRequestID(ctx), method, req, reply, cc, opts...)
	}
}

func requestIDStream() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withOutgoingRequestID(ctx), desc, cc, method, opts...)
	}
}

// timeoutUnary ограничивает unary-вызов без собственного дедлайна: keepalive
// ловит мёртвый транспорт, но не медленный сервер. Стримы живут по ctx —
// их длительность легитимно не ограничена.
func timeoutUnary(d time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if d <= 0 {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		if _, ok := ctx.Deadline(); ok {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func loggingUnary(log logkit.Logger) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		logCall(ctx, log, method, time.Since(start), err)
		return err
	}
}

// loggingStream пишет одну запись на стрим — в момент его завершения (EOF,
// ошибка или отмена, замеченные на Recv/Send), потому что до этого исхода
// вызова ещё нет.
func loggingStream(log logkit.Logger) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		start := time.Now()
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			logCall(ctx, log, method, time.Since(start), err)
			return nil, err
		}
		return &loggedStream{ClientStream: cs, log: log, method: method, start: start}, nil
	}
}

type loggedStream struct {
	grpc.ClientStream
	log    logkit.Logger
	method string
	start  time.Time
	once   sync.Once
}

func (s *loggedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		s.finish(nil)
	default:
		s.finish(err)
	}
	return err
}

// SendMsg отдаёт io.EOF, когда стрим уже завершён: настоящий статус приходит из
// RecvMsg, поэтому такой исход здесь не логируется.
func (s *loggedStream) SendMsg(m any) error {
	err := s.ClientStream.SendMsg(m)
	if err != nil && !errors.Is(err, io.EOF) {
		s.finish(err)
	}
	return err
}

func (s *loggedStream) finish(err error) {
	s.once.Do(func() { logCall(s.Context(), s.log, s.method, time.Since(s.start), err) })
}

func logCall(ctx context.Context, log logkit.Logger, method string, dur time.Duration, err error) {
	code := status.Code(err)
	kv := []any{"method", method, "code", code.String(), "duration_ms", dur.Milliseconds()}
	switch {
	case err == nil:
		log.Debug(ctx, "grpc client call", kv...)
	case grpcstatus.IsServerFault(code):
		log.Error(ctx, "grpc client call failed", append(kv, "error", err.Error())...)
	default:
		log.Warn(ctx, "grpc client call failed", append(kv, "error", err.Error())...)
	}
}
