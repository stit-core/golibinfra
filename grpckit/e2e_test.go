package grpckit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/grpckit/testkit/grpctest"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

const (
	echoUnary  = "/test.Echo/Check"
	echoStream = "/test.Echo/Watch"
)

type logEntry struct {
	level string
	msg   string
	kv    map[string]any
}

type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLogger) add(level, msg string, kv []any) {
	e := logEntry{level: level, msg: msg, kv: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.kv[k] = kv[i+1]
		}
	}
	l.mu.Lock()
	l.entries = append(l.entries, e)
	l.mu.Unlock()
}

func (l *captureLogger) Debug(_ context.Context, msg string, kv ...any) { l.add("debug", msg, kv) }
func (l *captureLogger) Info(_ context.Context, msg string, kv ...any)  { l.add("info", msg, kv) }
func (l *captureLogger) Warn(_ context.Context, msg string, kv ...any)  { l.add("warn", msg, kv) }
func (l *captureLogger) Error(_ context.Context, msg string, kv ...any) { l.add("error", msg, kv) }
func (l *captureLogger) With(...any) logkit.Logger                      { return l }

// find возвращает последнюю запись с таким уровнем/сообщением и полем method.
func (l *captureLogger) find(level, msg, method string) (logEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if e.level == level && e.msg == msg && e.kv["method"] == method {
			return e, true
		}
	}
	return logEntry{}, false
}

type stubAuth struct{ err error }

func (s *stubAuth) Authenticate(ctx context.Context, _ string) (context.Context, error) {
	return ctx, s.err
}

// echoRegistrar регистрирует минимальный сервис поверх health-proto сообщений:
// gen-стабов у кита нет, а proto-кодек в тесте нужен настоящий.
func echoRegistrar(unary, stream func(ctx context.Context) error) grpckit.ServiceRegistrar {
	desc := &grpc.ServiceDesc{
		ServiceName: "test.Echo",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Check",
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := new(healthpb.HealthCheckRequest)
				if err := dec(in); err != nil {
					return nil, err
				}
				h := func(ctx context.Context, _ any) (any, error) {
					if err := unary(ctx); err != nil {
						return nil, err
					}
					return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
				}
				if interceptor == nil {
					return h(ctx, in)
				}
				return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: echoUnary}, h)
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Watch",
			ServerStreams: true,
			Handler: func(_ any, ss grpc.ServerStream) error {
				if err := ss.RecvMsg(new(healthpb.HealthCheckRequest)); err != nil {
					return err
				}
				if err := stream(ss.Context()); err != nil {
					return err
				}
				return ss.SendMsg(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
			},
		}},
		Metadata: "test",
	}
	return func(s *grpc.Server) { s.RegisterService(desc, struct{}{}) }
}

func startEcho(t *testing.T, log logkit.Logger, unary, stream func(ctx context.Context) error, opts ...grpckit.ServerOption) *grpc.ClientConn {
	t.Helper()
	opts = append(opts, grpckit.WithService(echoRegistrar(unary, stream)))
	srv, err := grpckit.New(grpckit.Config{MaxRecvMiB: 4, MaxSendMiB: 4, ShutdownGrace: time.Second}, log, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return grpctest.StartServer(t, srv)
}

func callUnary(ctx context.Context, conn *grpc.ClientConn) (metadata.MD, error) {
	var hdr metadata.MD
	err := conn.Invoke(ctx, echoUnary, &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{}, grpc.Header(&hdr))
	return hdr, err
}

func callStream(ctx context.Context, conn *grpc.ClientConn) error {
	st, err := conn.NewStream(ctx, &grpc.StreamDesc{StreamName: "Watch", ServerStreams: true}, echoStream)
	if err != nil {
		return err
	}
	if err := st.SendMsg(&healthpb.HealthCheckRequest{}); err != nil {
		return err
	}
	return st.RecvMsg(&healthpb.HealthCheckResponse{})
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAuthRejectionIsGenericAndLogged(t *testing.T) {
	log := &captureLogger{}
	ok := func(context.Context) error { return nil }
	conn := startEcho(t, log, ok, ok, grpckit.WithAuthenticator(&stubAuth{err: errors.New("token expired: kid=abc")}))
	ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs("authorization", "Bearer x"))

	for name, call := range map[string]func() error{
		"unary":  func() error { _, err := callUnary(ctx, conn); return err },
		"stream": func() error { return callStream(ctx, conn) },
	} {
		err := call()
		st := status.Convert(err)
		if st.Code() != codes.Unauthenticated {
			t.Fatalf("%s: code = %v, want Unauthenticated", name, st.Code())
		}
		if st.Message() != "unauthenticated" || strings.Contains(st.Message(), "kid=abc") {
			t.Fatalf("%s: message = %q leaks the authenticator detail", name, st.Message())
		}
	}
	for _, method := range []string{echoUnary, echoStream} {
		e, ok := log.find("warn", "grpc auth rejected", method)
		if !ok || !strings.Contains(e.kv["error"].(string), "kid=abc") {
			t.Errorf("%s: detail must be logged at warn, got %+v ok=%v", method, e, ok)
		}
	}
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health must stay auth-exempt: %v", err)
	}
}

func TestRequestIDPropagation(t *testing.T) {
	var mu sync.Mutex
	var seen string
	capture := func(ctx context.Context) error {
		mu.Lock()
		seen = requestid.From(ctx)
		mu.Unlock()
		return nil
	}
	conn := startEcho(t, logkit.Nop(), capture, capture)

	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}

	t.Run("valid incoming id is kept and echoed", func(t *testing.T) {
		ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs(requestid.Header, "client-42"))
		hdr, err := callUnary(ctx, conn)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if got() != "client-42" || strings.Join(hdr.Get(requestid.Header), ",") != "client-42" {
			t.Fatalf("ctx id = %q, header = %v", got(), hdr.Get(requestid.Header))
		}
	})

	t.Run("invalid incoming id is replaced by uuid v7", func(t *testing.T) {
		ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs(requestid.Header, strings.Repeat("x", 300)))
		hdr, err := callUnary(ctx, conn)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		id, err := uuid.Parse(got())
		if err != nil || id.Version() != 7 {
			t.Fatalf("ctx id = %q, want uuid v7 (%v)", got(), err)
		}
		if strings.Join(hdr.Get(requestid.Header), ",") != got() {
			t.Fatalf("header %v must echo generated id %q", hdr.Get(requestid.Header), got())
		}
	})

	t.Run("missing id is generated for streams too", func(t *testing.T) {
		if err := callStream(testCtx(t), conn); err != nil {
			t.Fatalf("stream: %v", err)
		}
		if id, err := uuid.Parse(got()); err != nil || id.Version() != 7 {
			t.Fatalf("stream ctx id = %q, want uuid v7 (%v)", got(), err)
		}
	})
}

func TestAccessLogLevelByCode(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		level string
		code  string
	}{
		{"ok", nil, "info", "OK"},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad"), "warn", "InvalidArgument"},
		{"not found", status.Error(codes.NotFound, "nope"), "warn", "NotFound"},
		{"permission denied", status.Error(codes.PermissionDenied, "no"), "warn", "PermissionDenied"},
		{"internal", status.Error(codes.Internal, "boom"), "error", "Internal"},
		{"unavailable", status.Error(codes.Unavailable, "down"), "error", "Unavailable"},
		{"unimplemented", status.Error(codes.Unimplemented, "todo"), "error", "Unimplemented"},
		{"plain error → Unknown", errors.New("plain"), "error", "Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &captureLogger{}
			fail := func(context.Context) error { return tc.err }
			conn := startEcho(t, log, fail, fail)
			_, _ = callUnary(testCtx(t), conn)
			_ = callStream(testCtx(t), conn)

			for _, method := range []string{echoUnary, echoStream} {
				msg := "grpc call failed"
				if tc.err == nil {
					msg = "grpc call"
				}
				e, ok := log.find(tc.level, msg, method)
				if !ok || e.kv["code"] != tc.code {
					t.Errorf("%s: want %s %q code=%s, got %+v ok=%v", method, tc.level, msg, tc.code, e, ok)
				}
				for _, lvl := range []string{"debug", "info", "warn", "error"} {
					if lvl == tc.level {
						continue
					}
					if _, dup := log.find(lvl, msg, method); dup {
						t.Errorf("%s: unexpected %s log entry", method, lvl)
					}
				}
			}
		})
	}
}
