package cachekit_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

// blackHoleRedis принимает соединения и молчит: так выглядит зависший Redis.
func blackHoleRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

func TestRedisCache_ContextDeadlineReachesSocket(t *testing.T) {
	cfg := validConfig()
	cfg.URL = "redis://" + blackHoleRedis(t)
	cfg.MaxRetries = -1
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cache, err := cachekit.NewRedisCache(cfg)
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = cache.Get(ctx, "k")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Get against a silent server must fail")
	}
	// Верхняя граница команды — дедлайн ctx, а не REDIS_READ_TIMEOUT.
	if elapsed > 2*time.Second {
		t.Fatalf("Get returned after %s, want ~200ms (ctx deadline)", elapsed)
	}
}

func TestRedisCache_ClosedClient(t *testing.T) {
	cfg := validConfig()
	cfg.URL = "redis://" + closedAddr(t)
	cache, err := cachekit.NewRedisCache(cfg)
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}

	if err := cache.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got %v", err)
	}

	ctx := context.Background()
	ops := map[string]func() error{
		"get":           func() error { _, _, err := cache.Get(ctx, "k"); return err },
		"set":           func() error { return cache.Set(ctx, "k", []byte("v"), time.Minute) },
		"setnx":         func() error { _, err := cache.SetNX(ctx, "k", []byte("v"), time.Minute); return err },
		"getdel":        func() error { _, _, err := cache.GetDel(ctx, "k"); return err },
		"comparedelete": func() error { _, err := cache.CompareAndDelete(ctx, "k", []byte("v")); return err },
		"compareset": func() error {
			_, err := cache.CompareAndSet(ctx, "k", []byte("v"), []byte("w"), time.Minute)
			return err
		},
		"delete":          func() error { return cache.Delete(ctx, "k") },
		"setwithtags":     func() error { return cache.SetWithTags(ctx, "k", []byte("v"), time.Minute, "t") },
		"setwithoutags":   func() error { return cache.SetWithTags(ctx, "k", []byte("v"), time.Minute) },
		"invalidatebytag": func() error { return cache.InvalidateByTag(ctx, "t") },
		"ping":            func() error { return cache.Ping(ctx) },
		"setjson":         func() error { return cachekit.SetJSON(ctx, cache, "k", 1, time.Minute) },
		"getjson":         func() error { _, _, err := cachekit.GetJSON[int](ctx, cache, "k"); return err },
		"probe":           func() error { return cachekit.Probe(cache).Run(ctx) },
		"idempotency store": func() error {
			_, _, err := cachekit.NewIdempotencyStore(cache).Reserve(ctx, "k", time.Minute)
			return err
		},
	}
	for name, op := range ops {
		err := op()
		if err == nil {
			t.Errorf("%s after Close: err = nil, want an error", name)
			continue
		}
		if !strings.HasPrefix(err.Error(), "cachekit: ") {
			t.Errorf("%s after Close: %v, want a cachekit-prefixed error", name, err)
		}
		if !errors.Is(err, redis.ErrClosed) {
			t.Errorf("%s after Close: %v, want a wrapped redis.ErrClosed", name, err)
		}
	}
	if err := cache.Delete(ctx); err != nil {
		t.Errorf("Delete without keys: %v, want nil", err)
	}
}

func TestJSONHelpers(t *testing.T) {
	ctx := context.Background()
	cache := cachekit.NewMemoryCache()

	if err := cache.Set(ctx, "broken", []byte("{not json"), 0); err != nil {
		t.Fatal(err)
	}
	v, found, err := cachekit.GetJSON[map[string]string](ctx, cache, "broken")
	if err == nil || found || v != nil {
		t.Fatalf("GetJSON on garbage: %v %v %v, want (nil, false, error)", v, found, err)
	}
	if _, found, err := cachekit.GetJSON[int](ctx, cache, "missing"); found || err != nil {
		t.Fatalf("GetJSON on miss: found=%v err=%v", found, err)
	}
	if err := cachekit.SetJSON(ctx, cache, "k", make(chan int), 0); err == nil {
		t.Fatal("SetJSON of an unmarshalable value must fail")
	}
}
