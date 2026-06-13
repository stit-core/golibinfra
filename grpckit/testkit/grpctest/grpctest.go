// Package grpctest — bufconn-харнесс для тестов потребителей grpckit: поднимает
// grpckit.Server in-process (без реального порта) и отдаёт *grpc.ClientConn.
package grpctest

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/grpckit"
)

const bufSize = 1 << 20

// StartServer обслуживает srv поверх bufconn и возвращает клиентское соединение.
// Сервер и листенер закрываются в t.Cleanup.
func StartServer(t *testing.T, srv *grpckit.Server) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpctest: dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop(context.Background())
		_ = lis.Close()
	})
	return conn
}
