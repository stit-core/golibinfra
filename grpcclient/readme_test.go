package grpcclient_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// Рецепт bufconn из README § Тестирование потребитель копирует как есть, поэтому
// он собирается и проходит вызов: сигнатура dialer'а grpc-go и набор полей
// Config в примере закреплены здесь.
func TestReadmeBufconnRecipe(t *testing.T) {
	log := logkit.Nop()

	lis := bufconn.Listen(1 << 20)
	stub := grpc.NewServer()
	healthpb.RegisterHealthServer(stub, health.NewServer())
	go func() { _ = stub.Serve(lis) }()
	t.Cleanup(stub.Stop)

	conn, err := grpcclient.Dial(
		grpcclient.Config{Target: "passthrough:///bufnet", Token: "tok", AllowInsecureToken: true}, log,
		grpcclient.WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := healthpb.NewHealthClient(conn).Check(testCtx(t), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check over the README dialer: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.GetStatus())
	}
}
