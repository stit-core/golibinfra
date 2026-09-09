package cachekit_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

// closedAddr — гарантированно свободный адрес: connect в него отвергается сразу.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func offlineConfig(t *testing.T) cachekit.Config {
	cfg := validConfig()
	cfg.URL = "redis://" + closedAddr(t)
	cfg.DialTimeout = time.Second
	cfg.MaxRetries = -1
	return cfg
}

func TestModule_PingOnStartFailsFast(t *testing.T) {
	cfg := offlineConfig(t)
	cfg.PingOnStart = true

	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		cachekit.Module,
		healthkit.Provide(cachekit.Probe),
		fx.Invoke(func(cachekit.Cache) {}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := app.Start(ctx); err == nil {
		_ = app.Stop(ctx)
		t.Fatal("Start with REDIS_PING_ON_START=true and no Redis must fail")
	}
}

func TestModule_StartsWithoutPingAndClosesOnStop(t *testing.T) {
	cfg := offlineConfig(t)
	cfg.PingOnStart = false

	var cache cachekit.Cache
	var probes []healthkit.Probe
	app := fxtest.New(
		t,
		fx.Supply(cfg),
		cachekit.Module,
		healthkit.Provide(cachekit.Probe),
		fx.Populate(&cache),
		fx.Populate(fx.Annotate(&probes, fx.ParamTags(`group:"`+healthkit.Group+`"`))),
	)
	app.RequireStart()

	if len(probes) != 1 || probes[0].Name != "cachekit" {
		t.Fatalf("health group: %+v, want one cachekit probe", probes)
	}
	app.RequireStop()

	// OnStop закрыл клиент: дальше любая команда возвращает ошибку кита.
	if err := cache.Ping(context.Background()); err == nil {
		t.Fatal("Ping after OnStop must fail")
	}
}

// Сервер старше Redis 7 валит старт: тегированная инвалидация на нём проходит
// в новый тег и падает во второй записи. REDIS_SKIP_VERSION_CHECK — выход для
// redis-совместимых серверов.
func TestModule_RejectsOldRedisServer(t *testing.T) {
	cfg := validConfig()
	cfg.URL = testkit.StartRedis(t, testkit.WithImage("redis:6-alpine")).URL
	cfg.PingOnStart = true

	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		cachekit.Module,
		fx.Invoke(func(cachekit.Cache) {}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := app.Start(ctx)
	if !errors.Is(err, cachekit.ErrServerVersion) {
		_ = app.Stop(ctx)
		t.Fatalf("Start on Redis 6 = %v, want ErrServerVersion", err)
	}

	cfg.SkipVersionCheck = true
	skipping := fxtest.New(t, fx.Supply(cfg), cachekit.Module, fx.Invoke(func(cachekit.Cache) {}))
	skipping.RequireStart()
	skipping.RequireStop()
}

// На поддерживаемом сервере проверка версии не мешает старту.
func TestModule_StartsOnSupportedServer(t *testing.T) {
	cfg := validConfig()
	cfg.URL = testkit.StartRedis(t).URL
	cfg.PingOnStart = true

	app := fxtest.New(t, fx.Supply(cfg), cachekit.Module, fx.Invoke(func(cachekit.Cache) {}))
	app.RequireStart()
	app.RequireStop()
}

func TestModule_InvalidConfigStopsGraph(t *testing.T) {
	cfg := validConfig()
	cfg.PoolSize = 0

	app := fx.New(
		fx.NopLogger,
		fx.Supply(cfg),
		cachekit.Module,
		fx.Invoke(func(cachekit.Cache) {}),
	)
	if err := app.Err(); !errors.Is(err, cachekit.ErrConfig) {
		t.Fatalf("app.Err() = %v, want ErrConfig", err)
	}
}
