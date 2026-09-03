package oidckit

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
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

func TestCacheStateStore_OneTime(t *testing.T) {
	s := NewCacheStateStore(cachekit.NewMemoryCache())
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
