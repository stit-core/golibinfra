package cachekit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/akadmin/golibinfra/cachekit"
	"git.stit.tech/akadmin/golibinfra/testkit"
)

func TestRedisCache_GetSet(t *testing.T) {
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

	ctx := context.Background()
	key := "test-key"
	val := []byte("hello")

	if err := cache.Set(ctx, key, val, time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, found, err := cache.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("expected key to be found")
	}
	if string(got) != string(val) {
		t.Errorf("got %q, want %q", got, val)
	}
}

func TestRedisCache_MissReturnsNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartRedis(t)
	cache, err := cachekit.NewRedisCache(cachekit.Config{
		URL:         fix.URL,
		PoolSize:    5,
		DialTimeout: 5 * time.Second,
		ReadTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}

	ctx := context.Background()
	_, found, err := cache.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Error("expected not found")
	}
}

func TestRedisCache_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartRedis(t)
	cache, err := cachekit.NewRedisCache(cachekit.Config{
		URL:         fix.URL,
		PoolSize:    5,
		DialTimeout: 5 * time.Second,
		ReadTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}

	ctx := context.Background()
	_ = cache.Set(ctx, "k1", []byte("v1"), time.Minute)

	if err := cache.Delete(ctx, "k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, found, _ := cache.Get(ctx, "k1")
	if found {
		t.Error("expected key to be deleted")
	}
}

func TestGetSetJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	fix := testkit.StartRedis(t)
	cache, err := cachekit.NewRedisCache(cachekit.Config{
		URL:         fix.URL,
		PoolSize:    5,
		DialTimeout: 5 * time.Second,
		ReadTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}

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
	if err != nil {
		t.Fatalf("get json: %v", err)
	}
	if !found {
		t.Fatal("expected to find json key")
	}
	if got.Name != orig.Name || got.Age != orig.Age {
		t.Errorf("got %+v, want %+v", got, orig)
	}
}
