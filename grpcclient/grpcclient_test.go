package grpcclient

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/logkit"
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

func (l *captureLogger) find(level, msg string) (logEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.entries) - 1; i >= 0; i-- {
		if e := l.entries[i]; e.level == level && e.msg == msg {
			return e, true
		}
	}
	return logEntry{}, false
}

// failCodeKey — metadata-ключ, по которому тестовый сервер отвечает заданным статусом.
const failCodeKey = "x-fail-code"

// startServer поднимает health-сервер на bufconn и запоминает authorization
// каждого вызова.
func startServer(t *testing.T) (*bufconn.Listener, func() string) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	var mu sync.Mutex
	var auth string
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		mu.Lock()
		auth = strings.Join(md.Get("authorization"), ",")
		mu.Unlock()
		if v := md.Get(failCodeKey); len(v) > 0 {
			var c codes.Code
			if err := c.UnmarshalJSON([]byte(`"` + v[0] + `"`)); err != nil {
				return nil, status.Error(codes.Internal, "bad test code: "+err.Error())
			}
			return nil, status.Error(c, "forced")
		}
		return handler(ctx, req)
	}))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis, func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth
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

func TestDialBuildsConn(t *testing.T) {
	// grpc.NewClient ленивый — соединение не устанавливается, проверяем сборку.
	conn, err := Dial(Config{Target: "passthrough:///x", Token: "tok", TLS: true}, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if conn == nil {
		t.Fatal("nil conn")
	}
}

func TestBearerPerRPC(t *testing.T) {
	c := bearerPerRPC{token: "t", secure: false}
	md, err := c.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer t" {
		t.Fatalf("authorization = %q, want %q", md["authorization"], "Bearer t")
	}
	if c.RequireTransportSecurity() {
		t.Fatal("RequireTransportSecurity = true, want false for insecure")
	}
}

func TestBearerReachesServerAndPlaintextIsWarned(t *testing.T) {
	lis, seenAuth := startServer(t)
	log := &captureLogger{}
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet", Token: "tok"}, log)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if got := seenAuth(); got != "Bearer tok" {
		t.Fatalf("server saw authorization = %q, want %q", got, "Bearer tok")
	}
	e, ok := log.find("warn", "grpcclient: bearer token over plaintext transport")
	if !ok || e.kv["target"] != "passthrough:///bufnet" {
		t.Fatalf("plaintext bearer must be warned at Dial, got %+v ok=%v", e, ok)
	}
	if _, ok := log.find("debug", "grpc client call"); !ok {
		t.Fatal("successful call must be logged at debug")
	}
}

func TestNoTokenNoAuthorizationHeader(t *testing.T) {
	lis, seenAuth := startServer(t)
	conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if got := seenAuth(); got != "" {
		t.Fatalf("authorization = %q, want none", got)
	}
}

func TestTLSTokenIsNotWarned(t *testing.T) {
	log := &captureLogger{}
	conn, err := Dial(Config{Target: "passthrough:///x", Token: "tok", TLS: true}, log)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if _, ok := log.find("warn", "grpcclient: bearer token over plaintext transport"); ok {
		t.Fatal("bearer over TLS must not be warned")
	}
}

func TestAccessLogLevelByCode(t *testing.T) {
	lis, _ := startServer(t)
	cases := []struct {
		code  string
		level string
	}{
		{"NOT_FOUND", "warn"},
		{"INVALID_ARGUMENT", "warn"},
		{"UNAUTHENTICATED", "warn"},
		{"INTERNAL", "error"},
		{"UNAVAILABLE", "error"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			log := &captureLogger{}
			conn := dialBuf(t, lis, Config{Target: "passthrough:///bufnet"}, log)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, failCodeKey, tc.code)
			if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err == nil {
				t.Fatal("expected forced error")
			}
			if _, ok := log.find(tc.level, "grpc client call failed"); !ok {
				t.Fatalf("%s must be logged at %s: %+v", tc.code, tc.level, log.entries)
			}
		})
	}
}
