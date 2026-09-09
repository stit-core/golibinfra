package cachekit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

func TestMemoryCache_TTLAndNX(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	m := NewMemoryCache(WithClock(clk))

	if err := m.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.SetNX(ctx, "k", []byte("other"), time.Minute); ok {
		t.Fatal("SetNX on live key must fail")
	}
	clk.Advance(2 * time.Minute)
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
		t.Fatalf("stored value must be a copy of the argument, got %q", v)
	}
	v[0] = 'z'
	again, _, _ := m.Get(ctx, "k")
	if string(again) != "abc" {
		t.Fatalf("returned value must be a copy of the entry, got %q", again)
	}

	del, _, _ := m.GetDel(ctx, "k")
	del[0] = 'z'
	_ = m.Set(ctx, "k", []byte("abc"), 0)
	if v, _, _ := m.Get(ctx, "k"); string(v) != "abc" {
		t.Fatalf("GetDel must return a copy, got %q", v)
	}
}

func TestMemoryCache_DeleteDropsTagMembership(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCache()
	_ = m.SetWithTags(ctx, "a", []byte("1"), 0, "t")

	if err := m.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	tags := len(m.tags)
	m.mu.Unlock()
	if tags != 0 {
		t.Fatalf("tag sets after Delete: %d, want 0", tags)
	}
}

func TestMemoryCache_SweepsExpired(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	m := NewMemoryCache(WithClock(clk))

	const written = 2000
	for i := range written {
		key := strconv.Itoa(i)
		if err := m.SetWithTags(ctx, key, []byte("v"), time.Minute, "t"); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(2 * time.Minute)
	// Записи, которые никто не читает, должны уходить сами: иначе single-replica
	// потребитель (oidckit state, hubkit refresh) течёт по памяти.
	for i := range written {
		if err := m.Set(ctx, "fresh:"+strconv.Itoa(i), []byte("v"), time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	m.mu.Lock()
	entries, tagged := len(m.data), len(m.tags["t"])
	m.mu.Unlock()
	if entries > written+memorySweepMin {
		t.Fatalf("entries after sweep: %d, want <= %d", entries, written+memorySweepMin)
	}
	if tagged > memorySweepMin {
		t.Fatalf("tag members after sweep: %d, want <= %d", tagged, memorySweepMin)
	}
}

func TestMemoryCache_CompareAndDelete(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCache()
	_ = m.Set(ctx, "k", []byte("mine"), 0)

	if ok, err := m.CompareAndDelete(ctx, "k", []byte("other")); ok || err != nil {
		t.Fatalf("CompareAndDelete on foreign value: ok=%v err=%v", ok, err)
	}
	if _, found, _ := m.Get(ctx, "k"); !found {
		t.Fatal("foreign value must survive")
	}
	if ok, err := m.CompareAndDelete(ctx, "k", []byte("mine")); !ok || err != nil {
		t.Fatalf("CompareAndDelete on own value: ok=%v err=%v", ok, err)
	}
	if ok, err := m.CompareAndDelete(ctx, "missing", []byte("mine")); ok || err != nil {
		t.Fatalf("CompareAndDelete on missing key: ok=%v err=%v", ok, err)
	}
}

func TestMemoryCache_PingAndClose(t *testing.T) {
	m := NewMemoryCache()
	if err := m.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMemoryCache_Concurrent(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCache()

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Go(func() {
			for i := range 50 {
				key := fmt.Sprintf("k%d", (g+i)%17)
				_, _ = m.SetNX(ctx, key, []byte("v"), time.Minute)
				_ = m.SetWithTags(ctx, key, []byte("v"), time.Minute, "t")
				_, _, _ = m.Get(ctx, key)
				_, _, _ = m.GetDel(ctx, key)
				_, _ = m.CompareAndDelete(ctx, key, []byte("v"))
				_ = m.Delete(ctx, key)
				if i%25 == 0 {
					_ = m.InvalidateByTag(ctx, "t")
				}
			}
		})
	}
	wg.Wait()
}
