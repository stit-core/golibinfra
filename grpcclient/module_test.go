package grpcclient_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"git.stit.tech/stit-core/golibinfra/grpcclient"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestModuleProvidesConnAndClosesItOnStop(t *testing.T) {
	t.Setenv("GRPC_CLIENT_TARGET", "passthrough:///billing")

	var conn *grpc.ClientConn
	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(grpcclient.NewConfig),
		grpcclient.Module,
		fx.Populate(&conn),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if conn == nil {
		t.Fatal("Module must provide *grpc.ClientConn")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := conn.GetState(); got != connectivity.Shutdown {
		t.Fatalf("conn state after Stop = %v, want Shutdown", got)
	}
}

// Граф из README (§ Быстрый старт) должен собираться как написан.
func TestQuickStartGraphIsValid(t *testing.T) {
	t.Setenv("GRPC_CLIENT_TARGET", "passthrough:///billing")
	err := fx.ValidateApp(
		fx.NopLogger,
		logkit.Module,
		fx.Provide(logkit.NewConfig),
		fx.Provide(grpcclient.NewConfig),
		grpcclient.Module,
		fx.Invoke(func(*grpc.ClientConn) {}),
	)
	if err != nil {
		t.Fatalf("quick start graph: %v", err)
	}
}

func TestModuleFailsOnInvalidConfig(t *testing.T) {
	t.Setenv("GRPC_CLIENT_TARGET", "passthrough:///billing")
	t.Setenv("GRPC_CLIENT_TOKEN", "secret")

	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Provide(grpcclient.NewConfig),
		grpcclient.Module,
		fx.Invoke(func(*grpc.ClientConn) {}),
	)
	if err := app.Err(); !errors.Is(err, grpcclient.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}

// Config, собранный в коде мимо NewConfig, тоже валидируется — графом Module,
// а не первым вызовом.
func TestModuleRejectsSuppliedConfig(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(logkit.Nop),
		fx.Supply(grpcclient.Config{Target: "passthrough:///billing", Token: "secret"}),
		grpcclient.Module,
		fx.Invoke(func(*grpc.ClientConn) {}),
	)
	if err := app.Err(); !errors.Is(err, grpcclient.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}
