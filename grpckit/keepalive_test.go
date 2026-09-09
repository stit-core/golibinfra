package grpckit_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// clientPingTime — нижняя граница grpc-go: меньший интервал клампится, поэтому
// быстрее прогнать сценарий нельзя.
const clientPingTime = 10 * time.Second

// Сервер наказывает клиента, пингующего чаще EnforcementPolicy.MinTime: после
// третьего страйка соединение получает GOAWAY too_many_pings и умирает вместе с
// idle-стримом. Дефолты китов подобраны так, чтобы этого не случалось; тест
// повторяет то же соотношение на коротких интервалах и проверяет обе стороны.
func TestKeepaliveEnforcementVsClientPings(t *testing.T) {
	// Гейт по стенному времени, а не по Docker: сценарию нужны три страйка
	// с интервалом clientPingTime, ускорить его нечем (grpc-go клампит
	// PingTime снизу). Контейнеров тест не поднимает — testkit.RequireDocker
	// его не покрывает, поэтому гейт свой.
	if testing.Short() {
		t.Skip("keepalive strikes take ~45s of wall clock")
	}

	cases := []struct {
		name          string
		serverMinTime time.Duration
		wantCode      codes.Code
	}{
		{"client pings within the server policy", clientPingTime / 2, codes.DeadlineExceeded},
		{"client pings more often than the policy allows", 6 * clientPingTime, codes.Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			watch := idleHealthWatch(t, tc.serverMinTime)
			ctx, cancel := context.WithTimeout(context.Background(), 4*clientPingTime)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, err := watch.Recv()
				done <- err
			}()

			var err error
			select {
			case err = <-done:
			case <-ctx.Done():
				err = status.Error(codes.DeadlineExceeded, "stream is still alive")
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("idle stream ended with %v (%v), want %v", got, err, tc.wantCode)
			}
			if tc.wantCode == codes.Unavailable && !strings.Contains(err.Error(), "too_many_pings") {
				t.Fatalf("stream died of %v, want too_many_pings", err)
			}
		})
	}
}

// idleHealthWatch поднимает сервер с заданной политикой и открывает стрим,
// по которому не идёт трафик: страйки пинга сбрасывает только запись сервера.
func idleHealthWatch(t *testing.T, serverMinTime time.Duration) healthpb.Health_WatchClient {
	t.Helper()

	srv, err := grpckit.New(grpckit.Config{
		ShutdownGrace: time.Second,
		Keepalive:     grpckit.KeepaliveConfig{MinTime: serverMinTime},
	}, logkit.Nop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop(context.Background())
		_ = lis.Close()
	})

	conn, err := grpcclient.Dial(
		grpcclient.Config{
			Target:    "passthrough:///bufnet",
			Keepalive: grpcclient.KeepaliveConfig{Time: clientPingTime},
		},
		logkit.Nop(),
		grpcclient.WithDialOptions(grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watch, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{Service: "idle"})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := watch.Recv(); err != nil { // первый апдейт приходит сразу, дальше стрим молчит
		t.Fatalf("first watch update: %v", err)
	}
	return watch
}
