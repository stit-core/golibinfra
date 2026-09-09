package grpcclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

const (
	// failCodeKey — metadata-ключ, по которому тестовый сервер отвечает заданным статусом.
	failCodeKey = "x-fail-code"
	// delayKey — metadata-ключ задержки ответа (проверка дедлайнов).
	delayKey = "x-delay"
	// streamerService — сервис тестового сервера с bidi-методом Chat.
	streamerService = "grpcclient.test.Streamer"
	streamerMethod  = "/" + streamerService + "/Chat"
)

// startServer поднимает health-сервер на bufconn и запоминает metadata каждого
// вызова.
func startServer(t *testing.T) (*bufconn.Listener, func() metadata.MD) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	seen := make(chan metadata.MD, 64)
	last := metadata.MD{}

	handle := func(ctx context.Context) error {
		md, _ := metadata.FromIncomingContext(ctx)
		select {
		case seen <- md:
		default:
		}
		if v := md.Get(delayKey); len(v) > 0 {
			d, err := time.ParseDuration(v[0])
			if err != nil {
				return status.Error(codes.Internal, "bad test delay: "+err.Error())
			}
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if v := md.Get(failCodeKey); len(v) > 0 {
			var c codes.Code
			if err := c.UnmarshalJSON([]byte(`"` + v[0] + `"`)); err != nil {
				return status.Error(codes.Internal, "bad test code: "+err.Error())
			}
			return status.Error(c, "forced")
		}
		return nil
	}

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := handle(ctx); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := handle(ss.Context()); err != nil {
				return err
			}
			return handler(srv, ss)
		}),
	)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	// Health.Watch живёт до разрыва: чтобы проверить штатное завершение стрима,
	// нужен метод, который закрывает его сам.
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: streamerService,
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName:    "Chat",
			Handler:       func(any, grpc.ServerStream) error { return nil },
			ServerStreams: true,
			ClientStreams: true,
		}},
	}, nil)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis, func() metadata.MD {
		for {
			select {
			case md := <-seen:
				last = md
			default:
				return last
			}
		}
	}
}

func dialBuf(t *testing.T, lis *bufconn.Listener, cfg Config, log logkit.Logger) *grpc.ClientConn {
	t.Helper()
	conn, err := Dial(cfg, log, WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDialAppliesDefaults(t *testing.T) {
	// grpc.NewClient ленивый — соединение не устанавливается, проверяем сборку.
	conn, err := Dial(Config{Target: "passthrough:///x", Token: "tok", TLS: true}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if got := conn.Target(); got != "passthrough:///x" {
		t.Fatalf("target = %q", got)
	}
}

func TestDialRejectsInvalidConfig(t *testing.T) {
	if _, err := Dial(Config{}, nil); err == nil {
		t.Fatal("Dial with an empty target = nil, want ErrConfig")
	}
}

func TestBearerPerRPC(t *testing.T) {
	insecureCreds := bearerPerRPC{token: "t", secure: false}
	md, err := insecureCreds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer t" {
		t.Fatalf("authorization = %q, want %q", md["authorization"], "Bearer t")
	}
	if insecureCreds.RequireTransportSecurity() {
		t.Fatal("RequireTransportSecurity = true, want false for plaintext")
	}
	if !(bearerPerRPC{token: "t", secure: true}).RequireTransportSecurity() {
		t.Fatal("RequireTransportSecurity = false, want true under TLS")
	}
}

func TestBearerReachesServerAndPlaintextIsWarned(t *testing.T) {
	lis, seen := startServer(t)
	rec := logtest.Capture()
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet", Token: "tok", AllowInsecureToken: true}, rec)

	if _, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if got := strings.Join(seen().Get("authorization"), ","); got != "Bearer tok" {
		t.Fatalf("server saw authorization = %q, want %q", got, "Bearer tok")
	}
	e, ok := rec.Find(slog.LevelWarn, "grpcclient: bearer token over plaintext transport")
	if !ok || e.Str("target") != "passthrough:///bufnet" {
		t.Fatalf("plaintext bearer must be warned at Dial, got %+v ok=%v", e, ok)
	}
	if !rec.Has(slog.LevelDebug, "grpc client call") {
		t.Fatal("successful call must be logged at debug")
	}
}

func TestNoTokenNoAuthorizationHeader(t *testing.T) {
	lis, seen := startServer(t)
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, nil)

	if _, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if got := seen().Get("authorization"); len(got) != 0 {
		t.Fatalf("authorization = %q, want none", got)
	}
}

func TestTLSTokenIsNotWarned(t *testing.T) {
	rec := logtest.Capture()
	conn, err := Dial(Config{Target: "passthrough:///x", Token: "tok", TLS: true}, rec)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if rec.Has(slog.LevelWarn, "grpcclient: bearer token over plaintext transport") {
		t.Fatal("bearer over TLS must not be warned")
	}
}

func TestAccessLogLevelByCode(t *testing.T) {
	lis, _ := startServer(t)
	cases := []struct {
		code  string
		level slog.Level
	}{
		{"NOT_FOUND", slog.LevelWarn},
		{"INVALID_ARGUMENT", slog.LevelWarn},
		{"UNAUTHENTICATED", slog.LevelWarn},
		{"INTERNAL", slog.LevelError},
		{"UNAVAILABLE", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			rec := logtest.Capture()
			conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)
			ctx := metadata.AppendToOutgoingContext(testCtx(t), failCodeKey, tc.code)
			if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err == nil {
				t.Fatal("expected forced error")
			}
			e, ok := rec.Find(tc.level, "grpc client call failed")
			if !ok {
				t.Fatalf("%s must be logged at %v: %+v", tc.code, tc.level, rec.Entries())
			}
			if e.Str("method") != healthpb.Health_Check_FullMethodName {
				t.Fatalf("method = %q", e.Str("method"))
			}
		})
	}
}

// Стрим тоже попадает в access-log — записью в момент завершения.
func TestStreamAccessLog(t *testing.T) {
	lis, _ := startServer(t)

	t.Run("ok on stream end", func(t *testing.T) {
		rec := logtest.Capture()
		conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)
		watch, err := healthpb.NewHealthClient(conn).Watch(testCtx(t), &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
		if _, err := watch.Recv(); err != nil {
			t.Fatalf("first update: %v", err)
		}
		if rec.Has(slog.LevelDebug, "grpc client call") {
			t.Fatal("a live stream must not be logged before it ends")
		}

		if err := conn.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		for {
			if _, err := watch.Recv(); err != nil {
				break
			}
		}
		e, ok := rec.FindFunc(func(e logtest.Entry) bool {
			return strings.HasPrefix(e.Msg, "grpc client call")
		})
		if !ok || e.Str("method") != healthpb.Health_Watch_FullMethodName {
			t.Fatalf("stream must reach the access log, got %+v ok=%v", e, ok)
		}
	})

	t.Run("failed stream is logged with its code", func(t *testing.T) {
		rec := logtest.Capture()
		conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)
		ctx := metadata.AppendToOutgoingContext(testCtx(t), failCodeKey, "UNAVAILABLE")
		watch, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
		if _, err := watch.Recv(); err == nil || !errIsCode(err, codes.Unavailable) {
			t.Fatalf("recv err = %v, want Unavailable", err)
		}
		e, ok := rec.Find(slog.LevelError, "grpc client call failed")
		if !ok || e.Str("code") != codes.Unavailable.String() {
			t.Fatalf("stream failure must be logged at error, got %+v ok=%v", e, ok)
		}
	})
}

// Исход стрима приходит и без ошибки транспорта: штатный конец — io.EOF на
// Recv, отказ Send — ошибка вне io.EOF. Обе ветки дают ровно одну запись.
func TestStreamFinishReasons(t *testing.T) {
	lis, _ := startServer(t)
	desc := &grpc.StreamDesc{StreamName: "Chat", ServerStreams: true, ClientStreams: true}

	t.Run("clean end on EOF", func(t *testing.T) {
		rec := logtest.Capture()
		conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)

		cs, err := conn.NewStream(testCtx(t), desc, streamerMethod)
		if err != nil {
			t.Fatalf("NewStream: %v", err)
		}
		if err := cs.RecvMsg(&healthpb.HealthCheckResponse{}); !errors.Is(err, io.EOF) {
			t.Fatalf("RecvMsg = %v, want io.EOF", err)
		}
		e, ok := rec.Find(slog.LevelDebug, "grpc client call")
		if !ok || e.Str("code") != codes.OK.String() || e.Str("method") != streamerMethod {
			t.Fatalf("clean stream end must be logged at debug with code OK, got %+v ok=%v", e, ok)
		}
	})

	t.Run("send failure", func(t *testing.T) {
		rec := logtest.Capture()
		conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)

		cs, err := conn.NewStream(testCtx(t), desc, streamerMethod)
		if err != nil {
			t.Fatalf("NewStream: %v", err)
		}
		// Не proto-сообщение: SendMsg падает на маршалинге, а не на транспорте.
		if err := cs.SendMsg(struct{}{}); err == nil {
			t.Fatal("SendMsg of a non-proto message = nil, want an error")
		}
		e, ok := rec.FindFunc(func(e logtest.Entry) bool { return e.Msg == "grpc client call failed" })
		if !ok || e.Str("method") != streamerMethod {
			t.Fatalf("send failure must reach the access log, got %+v ok=%v", e, ok)
		}
	})
}

func errIsCode(err error, c codes.Code) bool {
	if errors.Is(err, io.EOF) {
		return c == codes.OK
	}
	return status.Code(err) == c
}

// Unary без дедлайна ограничивается GRPC_CLIENT_TIMEOUT: keepalive ловит
// мёртвый транспорт, но не медленный сервер.
func TestDefaultTimeoutForUnaryWithoutDeadline(t *testing.T) {
	lis, _ := startServer(t)
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet", Timeout: 50 * time.Millisecond}, nil)
	client := healthpb.NewHealthClient(conn)

	slow := metadata.AppendToOutgoingContext(context.Background(), delayKey, "3s")
	start := time.Now()
	_, err := client.Check(slow, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("code = %v, want DeadlineExceeded", status.Code(err))
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("call took %s, want the default deadline to fire", took)
	}

	// Дедлайн из ctx не перетирается: он строже или мягче по решению вызывающего.
	own, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Check(metadata.AppendToOutgoingContext(own, delayKey, "200ms"), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("call with its own deadline: %v", err)
	}
}

func TestZeroTimeoutLeavesCallWithoutDeadline(t *testing.T) {
	lis, _ := startServer(t)
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet", Timeout: 0}, nil)

	ctx := metadata.AppendToOutgoingContext(context.Background(), delayKey, "100ms")
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("call without a deadline: %v", err)
	}
}

func TestMaxRecvMsgSizeLimit(t *testing.T) {
	lis, _ := startServer(t)
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet", MaxRecvMsgSize: 1}, nil)
	_, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted", status.Code(err))
	}
}

// Атрибуты клиентского спана — то, что реально эмитит otelgrpc под дефолтной
// semconv (README § Observability); заодно проверяет, что опции хендлера
// доезжают до otelgrpc.
func TestStatsHandlerOptionsAndSpanAttributes(t *testing.T) {
	lis, _ := startServer(t)
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	conn, err := Dial(Config{Target: "passthrough:///bufnet"}, nil,
		WithStatsHandlerOptions(otelgrpc.WithTracerProvider(tp)),
		WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("check: %v", err)
	}

	ended := spans.Ended()
	if len(ended) == 0 {
		t.Fatal("client span must be recorded through the option")
	}
	span := ended[0]
	if span.Name() != "grpc.health.v1.Health/Check" {
		t.Fatalf("span name = %q", span.Name())
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value.String()
	}
	want := map[attribute.Key]string{
		"rpc.system.name":          "grpc",
		"rpc.method":               "grpc.health.v1.Health/Check",
		"rpc.response.status_code": "OK",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("span attribute %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
	if _, ok := attrs["server.address"]; !ok {
		t.Errorf("client span must carry server.address, got %v", attrs)
	}
}

// Стрим, который не удалось открыть, тоже попадает в access-log: исход известен
// сразу.
func TestStreamCreationFailureIsLogged(t *testing.T) {
	lis, _ := startServer(t)
	rec := logtest.Capture()
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, rec)
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := healthpb.NewHealthClient(conn).Watch(testCtx(t), &healthpb.HealthCheckRequest{}); err == nil {
		t.Fatal("watch on a closed conn = nil, want error")
	}
	e, ok := rec.FindFunc(func(e logtest.Entry) bool {
		return e.Msg == "grpc client call failed" && e.Str("method") == healthpb.Health_Watch_FullMethodName
	})
	if !ok {
		t.Fatalf("failed stream creation must be logged, entries: %+v", rec.Entries())
	}
	if e.Str("code") == codes.OK.String() {
		t.Fatalf("logged code = %q, want a failure code", e.Str("code"))
	}
}
