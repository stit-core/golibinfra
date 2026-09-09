package grpckit

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

type stubAuth struct {
	err    error
	got    string
	called bool
}

type authedKey struct{}

func (s *stubAuth) Authenticate(ctx context.Context, token string) (context.Context, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	s.got = token
	return context.WithValue(ctx, authedKey{}, token), nil
}

func TestIsAuthExempt(t *testing.T) {
	cases := map[string]bool{
		"/grpc.health.v1.Health/Check":             true,
		"/grpc.reflection.v1.ServerReflection/Foo": true,
		"/svc.Completion/Stream":                   false,
	}
	for method, want := range cases {
		if got := isAuthExempt(method); got != want {
			t.Errorf("isAuthExempt(%q) = %v, want %v", method, got, want)
		}
	}
}

func TestBearerToken(t *testing.T) {
	mk := func(v string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs(authMetadataKey, v))
	}
	cases := map[string]string{
		"Bearer abc":  "abc",
		"bearer abc":  "abc",
		"BEARER abc":  "abc",
		"Bearer  abc": "abc",
		"Bearer abc ": "abc",
		"Bearer ":     "",
		"bearer":      "bearer",
		"raw-token":   "raw-token",
		"   ":         "",
		"":            "",
	}
	for header, want := range cases {
		if got := bearerToken(mk(header)); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
	if got := bearerToken(context.Background()); got != "" {
		t.Errorf("no metadata = %q, want empty", got)
	}
}

// «Bearer » без токена — это отсутствие токена: порт не должен вызываться.
func TestAuthenticateEmptyBearerDoesNotCallPort(t *testing.T) {
	a := &stubAuth{}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authMetadataKey, "Bearer "))
	_, err := authenticate(ctx, a, logkit.Nop(), "/svc/M")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
	if msg := status.Convert(err).Message(); msg != "missing bearer token" {
		t.Fatalf("message = %q, want missing bearer token", msg)
	}
	if a.called {
		t.Fatal("authenticator must not be called without a token")
	}
}

func TestAuthenticateMissingToken(t *testing.T) {
	_, err := authenticate(context.Background(), &stubAuth{}, logkit.Nop(), "/svc/M")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestAuthenticateOK(t *testing.T) {
	a := &stubAuth{}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authMetadataKey, "Bearer tok"))
	got, err := authenticate(ctx, a, logkit.Nop(), "/svc/M")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if a.got != "tok" {
		t.Fatalf("authenticator got %q, want tok", a.got)
	}
	if got.Value(authedKey{}) != "tok" {
		t.Fatal("ctx not enriched by authenticator")
	}
}

func TestAuthenticateRejects(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(authMetadataKey, "Bearer x"))
	_, err := authenticate(ctx, &stubAuth{err: errors.New("bad token: kid=42")}, logkit.Nop(), "/svc/M")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
	if msg := status.Convert(err).Message(); msg != "unauthenticated" {
		t.Fatalf("message = %q, must not leak the authenticator error", msg)
	}
}

// Zero-value Config должен давать рабочий сервер: лимит сообщения 0 байт отбил
// бы даже health-запрос.
func TestZeroConfigServesHealth(t *testing.T) {
	srv, err := New(Config{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.cfg.MaxRecvMsgSize != defaultMaxMsgSize || srv.cfg.ShutdownGrace != defaultShutdownGrace {
		t.Fatalf("defaults not applied: %+v", srv.cfg)
	}

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop(context.Background())

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}

func TestServeRecordsListenerAddr(t *testing.T) {
	srv, err := New(Config{Addr: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := srv.Addr(); got != "" {
		t.Fatalf("Addr before Serve = %q, want empty", got)
	}
	ln, err := net.Listen("tcp", srv.cfg.Addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Stop(context.Background())

	waitFor(t, "listener addr recorded", func() bool { return srv.Addr() == ln.Addr().String() })
}

// waitFor ждёт условия вместо фиксированной паузы: Serve и health-loop работают
// в своих горутинах, момент их первого шага не детерминирован.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
