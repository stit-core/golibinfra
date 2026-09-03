package cachekit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCache_TTLAndNX(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	m := NewMemoryCache()
	m.now = func() time.Time { return now }

	if err := m.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.SetNX(ctx, "k", []byte("other"), time.Minute); ok {
		t.Fatal("SetNX on live key must fail")
	}
	now = now.Add(2 * time.Minute)
	if _, found, _ := m.Get(ctx, "k"); found {
		t.Fatal("expired key must be a miss")
	}
	if ok, _ := m.SetNX(ctx, "k", []byte("other"), time.Minute); !ok {
		t.Fatal("SetNX on expired key must succeed")
	}
	if v, found, _ := m.GetDel(ctx, "k"); !found || string(v) != "other" {
		t.Fatalf("GetDel: %q %v", v, found)
	}
	if _, found, _ := m.Get(ctx, "k"); found {
		t.Fatal("GetDel must remove the key")
	}
}

func TestMemoryCache_Tags(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCache()
	_ = m.SetWithTags(ctx, "a", []byte("1"), 0, "t1", "t2")
	_ = m.SetWithTags(ctx, "b", []byte("2"), 0, "t2")
	_ = m.Set(ctx, "c", []byte("3"), 0)

	if err := m.InvalidateByTag(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b"} {
		if _, found, _ := m.Get(ctx, k); found {
			t.Errorf("%s must be invalidated", k)
		}
	}
	if _, found, _ := m.Get(ctx, "c"); !found {
		t.Error("untagged key must survive")
	}
	if err := m.InvalidateByTag(ctx, "missing"); err != nil {
		t.Errorf("missing tag is not an error: %v", err)
	}
}

func TestMemoryCache_ValueIsolated(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCache()
	src := []byte("abc")
	_ = m.Set(ctx, "k", src, 0)
	src[0] = 'z'
	v, _, _ := m.Get(ctx, "k")
	if string(v) != "abc" {
		t.Fatalf("stored value must be a copy, got %q", v)
	}
}
