package cachekit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func newRedis(t *testing.T) cachekit.Cache {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	fix := testkit.StartRedis(t)
	cache, err := cachekit.NewRedisCache(cachekit.Config{
		URL:          fix.URL,
		PoolSize:     5,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func TestRedisCache_GetSet(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()

	if err := cache.Set(ctx, "test-key", []byte("hello"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, found, err := cache.Get(ctx, "test-key")
	if err != nil || !found || string(got) != "hello" {
		t.Fatalf("get: %q %v %v", got, found, err)
	}
	if _, found, err := cache.Get(ctx, "nonexistent"); err != nil || found {
		t.Fatalf("miss: found=%v err=%v", found, err)
	}
}

func TestRedisCache_Delete(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()
	_ = cache.Set(ctx, "k1", []byte("v1"), time.Minute)
	if err := cache.Delete(ctx, "k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := cache.Get(ctx, "k1"); found {
		t.Error("expected key to be deleted")
	}
}

func TestRedisCache_SetNXAndGetDel(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()

	ok, err := cache.SetNX(ctx, "lock", []byte("1"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("first SetNX: ok=%v err=%v", ok, err)
	}
	if ok, _ := cache.SetNX(ctx, "lock", []byte("2"), time.Minute); ok {
		t.Fatal("second SetNX must fail")
	}
	v, found, err := cache.GetDel(ctx, "lock")
	if err != nil || !found || string(v) != "1" {
		t.Fatalf("GetDel: %q %v %v", v, found, err)
	}
	if _, found, _ := cache.GetDel(ctx, "lock"); found {
		t.Fatal("second GetDel must miss")
	}
}

func TestRedisCache_Tags(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()

	_ = cache.SetWithTags(ctx, "user:1", []byte("a"), time.Minute, "users")
	_ = cache.SetWithTags(ctx, "user:2", []byte("b"), time.Minute, "users", "vip")
	_ = cache.Set(ctx, "other", []byte("c"), time.Minute)

	if err := cache.InvalidateByTag(ctx, "users"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	for _, k := range []string{"user:1", "user:2"} {
		if _, found, _ := cache.Get(ctx, k); found {
			t.Errorf("%s must be invalidated", k)
		}
	}
	if _, found, _ := cache.Get(ctx, "other"); !found {
		t.Error("untagged key must survive")
	}
	if err := cache.InvalidateByTag(ctx, "missing"); err != nil {
		t.Errorf("missing tag: %v", err)
	}
}

func TestGetSetJSON(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()
	type Payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	orig := Payload{Name: "alice", Age: 30}
	if err := cachekit.SetJSON(ctx, cache, "json-key", orig, time.Minute); err != nil {
		t.Fatalf("set json: %v", err)
	}
	got, found, err := cachekit.GetJSON[Payload](ctx, cache, "json-key")
	if err != nil || !found || got != orig {
		t.Fatalf("get json: %+v %v %v", got, found, err)
	}
}

func TestRedisCache_ProbeAndClose(t *testing.T) {
	cache := newRedis(t)
	ctx := context.Background()
	if err := cachekit.Probe(cache)(ctx); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := cache.Ping(ctx); err == nil {
		t.Fatal("ping after close must fail")
	}
}
