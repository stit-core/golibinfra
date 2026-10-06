package grpckit_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"git.stit.tech/stit-core/golibinfra/grpckit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
)

// freeAddr занимает и сразу отдаёт порт: fx-тесту нужен адрес до старта графа,
// а Config.Addr читается из ENV.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func startApp(t *testing.T, opts ...fx.Option) *fx.App {
	t.Helper()
	app := fx.New(append([]fx.Option{
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(grpckit.NewConfig),
		grpckit.Module,
	}, opts...)...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	})
	return app
}

// Module сам форсит конструирование сервера: без fx.Invoke приложение стартует
// с незанятым портом, если в графе никто не зависит от *Server.
func TestModuleStartsServerWithoutExternalDependency(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("GRPC_ADDR", addr)
	app := startApp(t)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("port %s must accept connections after Start: %v", addr, err)
	}
	_ = conn.Close()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("port must be released after Stop")
	}
}

// Граф из README (§ Быстрый старт) должен собираться как написан.
func TestQuickStartGraphIsValid(t *testing.T) {
	err := fx.ValidateApp(
		fx.NopLogger,
		logkit.Module,
		fx.Provide(logkit.NewConfig),
		grpckit.Module,
		fx.Provide(grpckit.NewConfig),
		grpckit.ProvideService(func() grpckit.ServiceRegistrar { return func(*grpc.Server) {} }),
	)
	if err != nil {
		t.Fatalf("quick start graph: %v", err)
	}
}

// ProvideService кладёт в группу и один регистратор, и срез; конструктор другого типа валит
// сборку графа, а не теряет сервис.
func TestProvideService(t *testing.T) {
	type in struct {
		fx.In
		Services []grpckit.ServiceRegistrar `group:"grpckit.services"`
	}
	var got []grpckit.ServiceRegistrar
	noop := func(*grpc.Server) {}
	app := fx.New(fx.NopLogger,
		grpckit.ProvideService(func() grpckit.ServiceRegistrar { return noop }),
		grpckit.ProvideService(func() ([]grpckit.ServiceRegistrar, error) {
			return []grpckit.ServiceRegistrar{noop, noop}, nil
		}),
		fx.Invoke(func(i in) { got = i.Services }),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx.New: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("services in group: got %d, want 3", len(got))
	}
	for _, ctor := range []any{nil, noop, func() error { return nil }, func() func(*grpc.Server) { return noop }} {
		err := fx.New(fx.NopLogger, grpckit.ProvideService(ctor)).Err()
		if err == nil || !strings.Contains(err.Error(), "grpckit: ProvideService") {
			t.Fatalf("ProvideService(%T) = %v, want the type check error", ctor, err)
		}
	}
}

func TestModuleWiresServicesGroupAndHealthProbes(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("GRPC_ADDR", addr)
	t.Setenv("GRPC_HEALTH_INTERVAL", "10ms")

	probeErr := make(chan error, 1)
	probeErr <- errors.New("dependency is down")
	startApp(t,
		grpckit.ProvideService(func() grpckit.ServiceRegistrar { return echoRegistrar(ok, ok) }),
		healthkit.ProvideProbe(func() healthkit.Probe {
			return healthkit.New("dep", func(context.Context) error {
				err := <-probeErr
				probeErr <- err
				return err
			})
		}),
	)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := callUnary(testCtx(t), conn); err != nil {
		t.Fatalf("service from the fx group must be registered: %v", err)
	}

	client := healthpb.NewHealthClient(conn)
	overall := func() healthpb.HealthCheckResponse_ServingStatus {
		resp, err := client.Check(testCtx(t), &healthpb.HealthCheckRequest{})
		if err != nil {
			return healthpb.HealthCheckResponse_SERVICE_UNKNOWN
		}
		return resp.GetStatus()
	}
	waitFor(t, "NOT_SERVING from the health group probe", func() bool {
		return overall() == healthpb.HealthCheckResponse_NOT_SERVING
	})

	<-probeErr
	probeErr <- nil
	waitFor(t, "SERVING after the probe recovers", func() bool {
		return overall() == healthpb.HealthCheckResponse_SERVING
	})
}

func TestModuleFailsOnInvalidConfig(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT_FILE", "/tmp/cert.pem")
	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(grpckit.NewConfig),
		grpckit.Module,
	)
	if err := app.Err(); !errors.Is(err, grpckit.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}

// Config, собранный в коде мимо NewConfig, валидируется тем же графом.
func TestModuleRejectsSuppliedConfig(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Supply(grpckit.Config{TLSCertFile: "cert.pem"}),
		grpckit.Module,
	)
	if err := app.Err(); !errors.Is(err, grpckit.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}

// Authenticator в графе опционален, но заданный — навешивается: сервис из
// fx-группы не должен оказаться открытым только потому, что собран через Module.
func TestModuleAppliesAuthenticatorFromGraph(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("GRPC_ADDR", addr)
	startApp(t,
		grpckit.ProvideService(func() grpckit.ServiceRegistrar { return echoRegistrar(ok, ok) }),
		fx.Provide(func() grpckit.Authenticator { return &stubAuth{err: errors.New("nope")} }),
	)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := callUnary(testCtx(t), conn); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("call without a token = %v, want Unauthenticated", status.Code(err))
	}
}

// Занятый порт валит старт приложения, а не остаётся молчаливым «сервер поднят».
func TestModuleFailsWhenPortIsBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	t.Setenv("GRPC_ADDR", busy.Addr().String())

	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(grpckit.NewConfig),
		grpckit.Module,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	startErr := app.Start(ctx)
	if startErr == nil {
		_ = app.Stop(ctx)
		t.Fatal("start on a busy port = nil, want a listen error")
	}
	if !strings.Contains(startErr.Error(), "grpckit: listen") {
		t.Fatalf("start error = %v, want it to name the listen failure", startErr)
	}
}

// Serve, оборвавшийся сам по себе, виден в логе: иначе приложение живёт с
// портом, который никто не слушает.
func TestModuleLogsServeFailure(t *testing.T) {
	t.Setenv("GRPC_ADDR", freeAddr(t))
	rec := logtest.Capture()

	var srv *grpckit.Server
	app := fx.New(
		fx.NopLogger,
		fx.Provide(func() logkit.Logger { return rec }),
		fx.Provide(grpckit.NewConfig),
		grpckit.Module,
		fx.Populate(&srv),
	)
	if err := app.Err(); err != nil {
		t.Fatalf("fx: %v", err)
	}
	srv.Stop(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(ctx) })

	waitFor(t, "grpckit: serve stopped in the log", func() bool {
		return rec.Has(slog.LevelError, "grpckit: serve stopped")
	})
}
