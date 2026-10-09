package grpckit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
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
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/grpctest"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

const (
	echoUnary  = "/test.Echo/Check"
	echoStream = "/test.Echo/Watch"
)

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

func newEcho(t *testing.T, log logkit.Logger, unary, stream func(ctx context.Context) error, opts ...grpckit.ServerOption) *grpckit.Server {
	t.Helper()
	opts = append(opts, grpckit.WithService(echoRegistrar(unary, stream)))
	srv, err := grpckit.New(grpckit.Config{ShutdownGrace: time.Second}, log, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func startEcho(t *testing.T, log logkit.Logger, unary, stream func(ctx context.Context) error, opts ...grpckit.ServerOption) *grpc.ClientConn {
	t.Helper()
	return grpctest.StartServer(t, newEcho(t, log, unary, stream, opts...))
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
	// До io.EOF: access-log сервер пишет после возврата хендлера, статус уходит клиенту следом.
	for {
		if err := st.RecvMsg(&healthpb.HealthCheckResponse{}); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func ok(context.Context) error { return nil }

// findLog возвращает последнюю запись с таким уровнем, сообщением и методом.
func findLog(rec *logtest.Recorder, level slog.Level, msg, method string) (logtest.Entry, bool) {
	return rec.FindFunc(func(e logtest.Entry) bool {
		return e.Level == level && e.Msg == msg && e.Str("method") == method
	})
}

func TestAuthRejectionIsGenericAndLogged(t *testing.T) {
	rec := logtest.Capture()
	conn := startEcho(t, rec, ok, ok, grpckit.WithAuthenticator(&stubAuth{err: errors.New("token expired: kid=abc")}))
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
		e, found := findLog(rec, slog.LevelWarn, "grpc auth rejected", method)
		if !found || !strings.Contains(e.Str("error"), "kid=abc") {
			t.Errorf("%s: detail must be logged at warn, got %+v found=%v", method, e, found)
		}
		if _, found := findLog(rec, slog.LevelWarn, "grpc call failed", method); !found {
			t.Errorf("%s: rejected call must reach the access log", method)
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
		seen = grpckit.RequestIDFrom(ctx)
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
		ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs(grpckit.HeaderRequestID, "client-42"))
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
		level slog.Level
		code  string
	}{
		{"ok", nil, slog.LevelInfo, "OK"},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad"), slog.LevelWarn, "InvalidArgument"},
		{"not found", status.Error(codes.NotFound, "nope"), slog.LevelWarn, "NotFound"},
		{"permission denied", status.Error(codes.PermissionDenied, "no"), slog.LevelWarn, "PermissionDenied"},
		{"internal", status.Error(codes.Internal, "boom"), slog.LevelError, "Internal"},
		{"unavailable", status.Error(codes.Unavailable, "down"), slog.LevelError, "Unavailable"},
		{"unimplemented", status.Error(codes.Unimplemented, "todo"), slog.LevelError, "Unimplemented"},
		{"plain error → Unknown", errors.New("plain"), slog.LevelError, "Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := logtest.Capture()
			fail := func(context.Context) error { return tc.err }
			conn := startEcho(t, rec, fail, fail)
			_, _ = callUnary(testCtx(t), conn)
			_ = callStream(testCtx(t), conn)

			for _, method := range []string{echoUnary, echoStream} {
				msg := "grpc call failed"
				if tc.err == nil {
					msg = "grpc call"
				}
				e, found := findLog(rec, tc.level, msg, method)
				if !found || e.Str("code") != tc.code {
					t.Errorf("%s: want %v %q code=%s, got %+v found=%v", method, tc.level, msg, tc.code, e, found)
				}
				for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
					if lvl == tc.level {
						continue
					}
					if _, dup := findLog(rec, lvl, msg, method); dup {
						t.Errorf("%s: unexpected %v log entry", method, lvl)
					}
				}
			}
		})
	}
}

// Паника — тот класс инцидентов, где корреляция нужнее всего: access-log с
// codes.Internal и request_id должен быть и для неё. request_id в записи есть
// без импорта httpkit — атрибут регистрирует сам grpckit.
func TestPanicIsRecoveredAndLoggedWithRequestID(t *testing.T) {
	var buf syncBuffer
	log, err := logkit.New(logkit.Config{Level: "debug"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatalf("logkit.New: %v", err)
	}
	boom := func(context.Context) error { panic("boom") }
	conn := startEcho(t, log, boom, boom)
	ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs(requestid.Header, "client-42"))

	if _, err := callUnary(ctx, conn); status.Code(err) != codes.Internal {
		t.Fatalf("unary code = %v, want Internal", status.Code(err))
	}
	if err := callStream(ctx, conn); status.Code(err) != codes.Internal {
		t.Fatalf("stream code = %v, want Internal", status.Code(err))
	}

	records := buf.records(t)
	for _, method := range []string{echoUnary, echoStream} {
		accessLog := findRecord(records, "grpc call failed", method)
		if accessLog == nil {
			t.Fatalf("%s: panic must produce an access-log record, got %v", method, records)
		}
		if accessLog["code"] != codes.Internal.String() {
			t.Errorf("%s: access-log code = %v, want Internal", method, accessLog["code"])
		}
		if accessLog["request_id"] != "client-42" {
			t.Errorf("%s: access-log request_id = %v, want client-42", method, accessLog["request_id"])
		}
		panicLog := findRecord(records, "grpc panic recovered", method)
		if panicLog == nil {
			t.Fatalf("%s: panic record is missing", method)
		}
		if panicLog["request_id"] != "client-42" {
			t.Errorf("%s: panic record request_id = %v, want client-42", method, panicLog["request_id"])
		}
	}
}

// Успешный вызов тоже несёт request_id: поле в JSON появляется от атрибута
// grpckit, а не от httpkit — этот тест-бинарь его не импортирует.
func TestRequestIDReachesJSONLog(t *testing.T) {
	var buf syncBuffer
	log, err := logkit.New(logkit.Config{Level: "debug"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatalf("logkit.New: %v", err)
	}
	conn := startEcho(t, log, ok, ok)
	ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs(requestid.Header, "client-7"))
	if _, err := callUnary(ctx, conn); err != nil {
		t.Fatalf("call: %v", err)
	}

	rec := findRecord(buf.records(t), "grpc call", echoUnary)
	if rec == nil {
		t.Fatal("access-log record is missing")
	}
	if rec["request_id"] != "client-7" {
		t.Fatalf("request_id = %v, want client-7", rec["request_id"])
	}
}

// syncBuffer — sink лога, который тест читает при живом сервере: мост grpclog пишет в логгер
// сервера из горутин grpc-go и после ответа на вызов (смена состояния канала).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func findRecord(records []map[string]any, msg, method string) map[string]any {
	for _, record := range slices.Backward(records) {
		if record["msg"] == msg && record["method"] == method {
			return record
		}
	}
	return nil
}
