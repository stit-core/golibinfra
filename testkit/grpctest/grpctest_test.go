package grpctest_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/connectivity"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/grpctest"
)

func newServer(t *testing.T) *grpckit.Server {
	t.Helper()
	srv, err := grpckit.New(grpckit.Config{ShutdownGrace: 5 * time.Second}, logkit.Nop())
	if err != nil {
		t.Fatalf("grpckit.New: %v", err)
	}
	return srv
}

func TestStartServer_ServesRPC(t *testing.T) {
	srv := newServer(t)
	srv.SetServing("svc", true)
	conn := grpctest.StartServer(t, srv)

	resp, err := healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{Service: "svc"})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}

// Соединение и сервер снимаются в t.Cleanup: проверяем состояние после подтеста.
func TestStartServer_ClosesConnOnCleanup(t *testing.T) {
	var conn interface {
		GetState() connectivity.State
	}
	t.Run("inner", func(t *testing.T) {
		c := grpctest.StartServer(t, newServer(t))
		if _, err := healthpb.NewHealthClient(c).Check(t.Context(), &healthpb.HealthCheckRequest{}); err != nil {
			t.Fatalf("health check: %v", err)
		}
		conn = c
	})
	if got := conn.GetState(); got != connectivity.Shutdown {
		t.Fatalf("conn state after cleanup = %v, want Shutdown", got)
	}
}
