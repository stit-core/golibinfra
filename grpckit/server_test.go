package grpckit_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/grpctest"
)

// waitFor ждёт условия вместо фиксированной паузы: health-loop и GracefulStop
// работают в своих горутинах.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestWithUnaryInterceptorsRunAfterAuth(t *testing.T) {
	type tenantKey struct{}
	auth := grpckit.WithAuthenticator(authFunc(func(ctx context.Context, token string) (context.Context, error) {
		return context.WithValue(ctx, tenantKey{}, token), nil
	}))

	var mu sync.Mutex
	var seen any
	extra := grpckit.WithUnaryInterceptors(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		mu.Lock()
		seen = ctx.Value(tenantKey{})
		mu.Unlock()
		return handler(ctx, req)
	})

	conn := startEcho(t, logkit.Nop(), ok, ok, auth, extra)
	ctx := metadata.NewOutgoingContext(testCtx(t), metadata.Pairs("authorization", "Bearer tenant-1"))
	if _, err := callUnary(ctx, conn); err != nil {
		t.Fatalf("call: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen != "tenant-1" {
		t.Fatalf("custom interceptor saw %v, want the authenticated ctx value", seen)
	}
}

func TestWithStreamInterceptorsRun(t *testing.T) {
	var called bool
	var mu sync.Mutex
	extra := grpckit.WithStreamInterceptors(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		mu.Lock()
		called = true
		mu.Unlock()
		return handler(srv, ss)
	})
	conn := startEcho(t, logkit.Nop(), ok, ok, extra)
	if err := callStream(testCtx(t), conn); err != nil {
		t.Fatalf("stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("stream interceptor from WithStreamInterceptors was not called")
	}
}

// Опции потребителя применяются последними и побеждают одноимённые опции кита.
func TestWithServerOptionsOverrideKitDefaults(t *testing.T) {
	conn := startEcho(t, logkit.Nop(), ok, ok, grpckit.WithServerOptions(grpc.MaxSendMsgSize(1)))
	_, err := callUnary(testCtx(t), conn)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted from the consumer limit", status.Code(err))
	}
}

// Атрибуты спана — то, что реально эмитит otelgrpc под дефолтной semconv
// (docs/observability.md § Спаны).
func TestStatsHandlerOptionsAndSpanAttributes(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	conn := startEcho(t, logkit.Nop(), ok, ok, grpckit.WithStatsHandlerOptions(otelgrpc.WithTracerProvider(tp)))
	if _, err := callUnary(testCtx(t), conn); err != nil {
		t.Fatalf("call: %v", err)
	}

	waitFor(t, "server span", func() bool { return len(rec.Ended()) > 0 })
	span := rec.Ended()[0]
	if span.Name() != "test.Echo/Check" {
		t.Fatalf("span name = %q, want test.Echo/Check", span.Name())
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value.String()
	}
	want := map[attribute.Key]string{
		"rpc.system.name":          "grpc",
		"rpc.method":               "test.Echo/Check",
		"rpc.response.status_code": "OK",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("span attribute %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
}

func TestReflectionToggle(t *testing.T) {
	const service = "grpc.reflection.v1.ServerReflection"

	off := newEcho(t, logkit.Nop(), ok, ok)
	if _, found := off.GRPC().GetServiceInfo()[service]; found {
		t.Fatal("reflection must stay off by default")
	}

	on, err := grpckit.New(grpckit.Config{Reflection: true}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, found := on.GRPC().GetServiceInfo()[service]; !found {
		t.Fatalf("GRPC_REFLECTION=true must register %s", service)
	}
}

func TestSetServingPerService(t *testing.T) {
	srv := newEcho(t, logkit.Nop(), ok, ok)
	srv.SetServing("test.Echo", true)
	conn := grpctest.StartServer(t, srv)
	client := healthpb.NewHealthClient(conn)

	resp, err := client.Check(testCtx(t), &healthpb.HealthCheckRequest{Service: "test.Echo"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}

	srv.SetServing("test.Echo", false)
	resp, err = client.Check(testCtx(t), &healthpb.HealthCheckRequest{Service: "test.Echo"})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", resp.GetStatus())
	}
}

// Пробы группы health решают overall-статус: иначе оркестратор видит SERVING у
// сервиса с недоступной зависимостью.
func TestHealthProbesDriveOverallStatus(t *testing.T) {
	var mu sync.Mutex
	probeErr := errors.New("db is down")
	probe := healthkit.New("pgkit", func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return probeErr
	})

	srv, err := grpckit.New(
		grpckit.Config{HealthInterval: 10 * time.Millisecond, ShutdownGrace: time.Second},
		logkit.Nop(),
		grpckit.WithHealthProbes(probe),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn := grpctest.StartServer(t, srv)
	client := healthpb.NewHealthClient(conn)

	overall := func() healthpb.HealthCheckResponse_ServingStatus {
		resp, err := client.Check(testCtx(t), &healthpb.HealthCheckRequest{})
		if err != nil {
			return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
		}
		return resp.GetStatus()
	}
	waitFor(t, "NOT_SERVING on failing probe", func() bool {
		return overall() == healthpb.HealthCheckResponse_NOT_SERVING
	})

	mu.Lock()
	probeErr = nil
	mu.Unlock()
	waitFor(t, "SERVING after the probe recovers", func() bool {
		return overall() == healthpb.HealthCheckResponse_SERVING
	})
}

func TestHealthProbesDisabledWithoutInterval(t *testing.T) {
	probe := healthkit.New("pgkit", func(context.Context) error { return errors.New("down") })
	srv, err := grpckit.New(
		grpckit.Config{HealthInterval: 0, ShutdownGrace: time.Second},
		logkit.Nop(),
		grpckit.WithHealthProbes(probe),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn := grpctest.StartServer(t, srv)

	resp, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING while GRPC_HEALTH_INTERVAL=0", resp.GetStatus())
	}
}

// Stop уводит трафик до остановки (NOT_SERVING зрителям Watch) и форсирует
// остановку по истечении ShutdownGrace, даже если стрим висит.
func TestStopMarksNotServingAndForcesAfterGrace(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	srv, err := grpckit.New(grpckit.Config{ShutdownGrace: 200 * time.Millisecond}, logkit.Nop(),
		grpckit.WithService(echoRegistrar(ok, func(ctx context.Context) error {
			select {
			case <-hang:
			case <-ctx.Done():
			}
			return nil
		})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn := grpctest.StartServer(t, srv)

	watch, err := healthpb.NewHealthClient(conn).Watch(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	first, err := watch.Recv()
	if err != nil || first.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("first watch update = %v, %v; want SERVING", first.GetStatus(), err)
	}

	go func() { _ = callStream(context.Background(), conn) }()

	stopped := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		srv.Stop(context.Background())
		stopped <- time.Since(start)
	}()

	update, err := watch.Recv()
	if err != nil {
		t.Fatalf("watch after Stop: %v", err)
	}
	if update.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("watch update = %v, want NOT_SERVING", update.GetStatus())
	}

	select {
	case took := <-stopped:
		if took > 2*time.Second {
			t.Fatalf("Stop took %s, want a force stop right after the grace window", took)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return: the hanging stream must be force-stopped after the grace window")
	}
}

func TestStopHonoursCanceledContext(t *testing.T) {
	srv := newEcho(t, logkit.Nop(), ok, ok)
	_ = grpctest.StartServer(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		srv.Stop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop with a canceled ctx must force-stop immediately")
	}
}

func TestTLSServesOverEncryptedTransport(t *testing.T) {
	certFile, keyFile, pool := selfSignedCert(t)
	srv, err := grpckit.New(grpckit.Config{
		Addr:        "127.0.0.1:0",
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
	}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(context.Background()) })

	creds := credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health over TLS: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}

func selfSignedCert(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("append cert to pool")
	}
	return certFile, keyFile, pool
}

// authFunc — Authenticator из функции (стаб для тестов опций).
type authFunc func(ctx context.Context, token string) (context.Context, error)

func (f authFunc) Authenticate(ctx context.Context, token string) (context.Context, error) {
	return f(ctx, token)
}

// Health-стрим оркестратора проходит без токена даже при включённом
// Authenticator; доменный стрим — нет.
func TestStreamAuthExemptForHealth(t *testing.T) {
	deny := grpckit.WithAuthenticator(authFunc(func(context.Context, string) (context.Context, error) {
		return nil, errors.New("nope")
	}))
	conn := startEcho(t, logkit.Nop(), ok, ok, deny)

	watch, err := healthpb.NewHealthClient(conn).Watch(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	resp, err := watch.Recv()
	if err != nil {
		t.Fatalf("health watch must stay auth-exempt: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}

	if err := callStream(testCtx(t), conn); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("domain stream without a token = %v, want Unauthenticated", status.Code(err))
	}
}

// Стрим после успешной auth получает ctx порта: identity не должна теряться
// между интерсептором и handler'ом.
func TestStreamAuthPassesIdentityToHandler(t *testing.T) {
	type identityKey struct{}
	auth := grpckit.WithAuthenticator(authFunc(func(ctx context.Context, token string) (context.Context, error) {
		return context.WithValue(ctx, identityKey{}, token), nil
	}))

	seen := make(chan any, 1)
	conn := startEcho(t, logkit.Nop(), ok, func(ctx context.Context) error {
		seen <- ctx.Value(identityKey{})
		return nil
	}, auth)

	ctx := metadata.AppendToOutgoingContext(testCtx(t), "authorization", "Bearer let-me-in")
	if err := callStream(ctx, conn); err != nil {
		t.Fatalf("authenticated stream: %v", err)
	}
	if got := <-seen; got != "let-me-in" {
		t.Fatalf("stream handler ctx identity = %v, want the token validated by the port", got)
	}
}

// GRPC_MAX_CONCURRENT_STREAMS ограничивает число стримов на соединение: без
// лимита один клиент занимает сервер целиком.
func TestMaxConcurrentStreamsLimit(t *testing.T) {
	srv, err := grpckit.New(grpckit.Config{MaxConcurrentStreams: 1, ShutdownGrace: time.Second}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conn := grpctest.StartServer(t, srv)

	// Первый стрим занимает единственный слот; его ответ гарантирует, что
	// клиент уже получил SETTINGS сервера.
	watch, err := healthpb.NewHealthClient(conn).Watch(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatalf("first update: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("second stream = %v, want DeadlineExceeded while the only slot is busy", status.Code(err))
	}
}

// mTLS собирается опцией потребителя: grpc.Creds из WithServerOptions побеждает
// креды кита из GRPC_TLS_*.
func TestMutualTLSViaServerOptions(t *testing.T) {
	certFile, keyFile, pool := selfSignedCert(t)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("load keypair: %v", err)
	}

	srv, err := grpckit.New(grpckit.Config{}, logkit.Nop(), grpckit.WithServerOptions(
		grpc.Creds(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		})),
	))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(context.Background()) })

	dial := func(certs []tls.Certificate) error {
		conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			RootCAs:      pool,
			ServerName:   "localhost",
			Certificates: certs,
		})))
		if err != nil {
			return err
		}
		defer conn.Close()
		_, err = healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{})
		return err
	}

	if err := dial([]tls.Certificate{cert}); err != nil {
		t.Fatalf("client with a certificate must pass mTLS: %v", err)
	}
	if err := dial(nil); err == nil {
		t.Fatal("client without a certificate must be rejected")
	}
}
