package oidckit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStateStore_OneTime(t *testing.T) {
	s := NewMemoryStateStore()
	ctx := context.Background()

	if err := s.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := s.GetDel(ctx, "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("GetDel: v=%q ok=%v err=%v", v, ok, err)
	}
	// one-time: второй GetDel — miss.
	if _, ok, _ := s.GetDel(ctx, "k"); ok {
		t.Fatal("повторный GetDel вернул значение")
	}
}

func TestMemoryStateStore_TTL(t *testing.T) {
	s := NewMemoryStateStore()
	ctx := context.Background()

	if err := s.Set(ctx, "k", []byte("v"), -time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, ok, _ := s.GetDel(ctx, "k"); ok {
		t.Fatal("протухшая запись вернулась")
	}
}

// fakeCache — минимальный cachekit.Cache для проверки адаптера.
type fakeCache struct {
	m map[string][]byte
}

func (c *fakeCache) Get(_ context.Context, k string) ([]byte, bool, error) {
	v, ok := c.m[k]
	return v, ok, nil
}

func (c *fakeCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.m[k] = v
	return nil
}

func (c *fakeCache) Delete(_ context.Context, keys ...string) error {
	for _, k := range keys {
		delete(c.m, k)
	}
	return nil
}
func (c *fakeCache) InvalidateByTag(context.Context, string) error { return nil }
func (c *fakeCache) Ping(context.Context) error                    { return nil }

func TestCacheStateStore_OneTime(t *testing.T) {
	s := NewCacheStateStore(&fakeCache{m: map[string][]byte{}})
	ctx := context.Background()

	if err := s.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := s.GetDel(ctx, "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("GetDel: v=%q ok=%v err=%v", v, ok, err)
	}
	if _, ok, _ := s.GetDel(ctx, "k"); ok {
		t.Fatal("повторный GetDel вернул значение")
	}
}
