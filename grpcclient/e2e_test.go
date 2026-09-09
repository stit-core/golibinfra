package grpcclient_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Сквозная корреляция: id из ctx вызывающего доезжает до grpckit тем же
// metadata-ключом, поэтому downstream не генерирует свой.
func TestRequestIDReachesGrpckitServer(t *testing.T) {
	srv, err := grpckit.New(grpckit.Config{ShutdownGrace: time.Second}, logkit.Nop())
	if err != nil {
		t.Fatalf("grpckit.New: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop(context.Background())
		_ = lis.Close()
	})

	conn, err := grpcclient.Dial(
		grpcclient.Config{Target: "passthrough:///bufnet"},
		logkit.Nop(),
		grpcclient.WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	t.Run("id from ctx is forwarded", func(t *testing.T) {
		ctx := requestid.With(testCtx(t), "caller-99")
		var hdr metadata.MD
		if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&hdr)); err != nil {
			t.Fatalf("check: %v", err)
		}
		if got := strings.Join(hdr.Get(grpckit.HeaderRequestID), ","); got != "caller-99" {
			t.Fatalf("server echoed %q, want the caller id", got)
		}
	})

	t.Run("explicit metadata wins", func(t *testing.T) {
		ctx := metadata.AppendToOutgoingContext(requestid.With(testCtx(t), "caller-99"), grpckit.HeaderRequestID, "explicit-1")
		var hdr metadata.MD
		if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&hdr)); err != nil {
			t.Fatalf("check: %v", err)
		}
		if got := strings.Join(hdr.Get(grpckit.HeaderRequestID), ","); got != "explicit-1" {
			t.Fatalf("server echoed %q, want the explicit metadata value", got)
		}
	})

	t.Run("no id in ctx leaves it to the server", func(t *testing.T) {
		var hdr metadata.MD
		if _, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{}, grpc.Header(&hdr)); err != nil {
			t.Fatalf("check: %v", err)
		}
		if got := hdr.Get(grpckit.HeaderRequestID); len(got) != 1 || got[0] == "" {
			t.Fatalf("server must generate an id, got %v", got)
		}
	})
}

// CA и ServerName доезжают до tls.Config: сертификат внутреннего PKI не лежит в
// системном сторе, а target часто адресует под по IP.
func TestTLSWithCustomCAAndServerName(t *testing.T) {
	certFile, keyFile, caFile := selfSignedCert(t)

	srv, err := grpckit.New(grpckit.Config{TLSCertFile: certFile, TLSKeyFile: keyFile}, logkit.Nop())
	if err != nil {
		t.Fatalf("grpckit.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(context.Background()) })

	conn, err := grpcclient.Dial(grpcclient.Config{
		Target:        ln.Addr().String(),
		TLS:           true,
		TLSCAFile:     caFile,
		TLSServerName: "localhost",
	}, logkit.Nop())
	if err != nil {
		t.Fatalf("Dial: %v", err)
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

func TestTLSCAFileErrors(t *testing.T) {
	if _, err := grpcclient.Dial(grpcclient.Config{Target: "passthrough:///x", TLS: true, TLSCAFile: "/no/such/ca.pem"}, nil); err == nil {
		t.Fatal("missing CA file must fail Dial")
	}

	junk := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(junk, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := grpcclient.Dial(grpcclient.Config{Target: "passthrough:///x", TLS: true, TLSCAFile: junk}, nil)
	if !errors.Is(err, grpcclient.ErrConfig) {
		t.Fatalf("Dial with a junk CA = %v, want ErrConfig", err)
	}
}

func selfSignedCert(t *testing.T) (certFile, keyFile, caFile string) {
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
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
	caFile = filepath.Join(dir, "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for name, content := range map[string][]byte{
		certFile: certPEM,
		caFile:   certPEM,
		keyFile:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(name, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile, caFile
}
