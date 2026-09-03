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
	err error
	got string
}

type authedKey struct{}

func (s *stubAuth) Authenticate(ctx context.Context, token string) (context.Context, error) {
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
	if got := bearerToken(mk("Bearer abc")); got != "abc" {
		t.Errorf("Bearer-prefixed = %q, want abc", got)
	}
	if got := bearerToken(mk("raw-token")); got != "raw-token" {
		t.Errorf("raw = %q, want raw-token", got)
	}
	if got := bearerToken(context.Background()); got != "" {
		t.Errorf("no metadata = %q, want empty", got)
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

func TestIsServerFault(t *testing.T) {
	for _, c := range []codes.Code{codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.Unimplemented} {
		if !isServerFault(c) {
			t.Errorf("%v must be a server fault", c)
		}
	}
	for _, c := range []codes.Code{codes.OK, codes.InvalidArgument, codes.NotFound, codes.Unauthenticated, codes.PermissionDenied, codes.Canceled, codes.DeadlineExceeded, codes.ResourceExhausted} {
		if isServerFault(c) {
			t.Errorf("%v must not be a server fault", c)
		}
	}
}

func TestHealthEndToEnd(t *testing.T) {
	srv, err := New(Config{MaxRecvMiB: 4, MaxSendMiB: 4, ShutdownGrace: time.Second}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
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
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.Status)
	}
}
