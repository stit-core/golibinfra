package cachekit

import (
	"context"
	"errors"
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

// Регрессия KTST-05: членство в теге переживает ключ, как в Redis-наборе tag:<tag>. Ключ, который
// удалён или истёк и записан заново без тега, InvalidateByTag всё равно сносит.
func TestMemoryCache_TagMembershipOutlivesKey(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	gone := map[string]func(m *MemoryCache){
		"Delete":           func(m *MemoryCache) { _ = m.Delete(ctx, "k") },
		"GetDel":           func(m *MemoryCache) { _, _, _ = m.GetDel(ctx, "k") },
		"CompareAndDelete": func(m *MemoryCache) { _, _ = m.CompareAndDelete(ctx, "k", []byte("v1")) },
		"expiry":           func(*MemoryCache) { clk.Advance(2 * time.Minute) },
		"overwrite":        func(*MemoryCache) {},
	}
	for name, remove := range gone {
		m := NewMemoryCache(WithClock(clk))
		_ = m.SetWithTags(ctx, "k", []byte("v1"), time.Minute, "t")
		_ = m.SetWithTags(ctx, "keeper", []byte("v"), time.Hour, "t")
		remove(m)
		if err := m.Set(ctx, "k", []byte("v2"), 0); err != nil {
			t.Fatal(err)
		}
		if err := m.InvalidateByTag(ctx, "t"); err != nil {
			t.Fatal(err)
		}
		if v, found, _ := m.Get(ctx, "k"); found {
			t.Errorf("%s: the key written again without the tag survived InvalidateByTag: %q", name, v)
		}
	}
}

// Набор тега живёт не меньше самого долгоживущего члена, как Redis-набор под PEXPIRE … GT: с
// концом набора уходит и членство — запись без TTL делает его бессрочным.
func TestMemoryCache_TagSetLifetime(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		members []time.Duration // TTL записей SetWithTags по порядку
		wait    time.Duration
		alive   bool // набор жив: InvalidateByTag сносит ключ, записанный заново
	}{
		{"expires with its only member", []time.Duration{time.Minute}, time.Minute, false},
		{"alive just before the member expires", []time.Duration{time.Minute}, time.Minute - time.Nanosecond, true},
		{"a longer member extends the set", []time.Duration{time.Minute, time.Hour}, 2 * time.Minute, true},
		{"a shorter member does not shorten it", []time.Duration{time.Hour, time.Minute}, 2 * time.Minute, true},
		{"extended set still expires", []time.Duration{time.Minute, time.Hour}, time.Hour, false},
		{"a member without ttl makes it persistent", []time.Duration{time.Minute, 0}, 1000 * time.Hour, true},
		{"a persistent set stays persistent", []time.Duration{0, time.Minute}, 1000 * time.Hour, true},
		{"persistent from the first write", []time.Duration{0}, 1000 * time.Hour, true},
	}
	for _, tc := range cases {
		clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
		m := NewMemoryCache(WithClock(clk))
		for i, ttl := range tc.members {
			if err := m.SetWithTags(ctx, "k"+strconv.Itoa(i), []byte("v"), ttl, "t"); err != nil {
				t.Fatal(err)
			}
		}
		clk.Advance(tc.wait)
		_ = m.Set(ctx, "k0", []byte("again"), 0)
		if err := m.InvalidateByTag(ctx, "t"); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := m.Get(ctx, "k0"); found == tc.alive {
			t.Errorf("%s: key found = %v after InvalidateByTag, want the tag set alive = %v", tc.name, found, tc.alive)
		}
	}
}

// Истёкший набор новая запись начинает заново: прежнего членства в нём нет.
func TestMemoryCache_ExpiredTagSetStartsFresh(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	m := NewMemoryCache(WithClock(clk))
	_ = m.SetWithTags(ctx, "old", []byte("v"), time.Minute, "t")
	clk.Advance(time.Hour)
	_ = m.Set(ctx, "old", []byte("again"), 0)
	_ = m.SetWithTags(ctx, "new", []byte("v"), time.Minute, "t")

	if err := m.InvalidateByTag(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := m.Get(ctx, "old"); !found {
		t.Error("a member of the expired tag set was invalidated through the new one")
	}
	if _, found, _ := m.Get(ctx, "new"); found {
		t.Error("the member of the new tag set survived InvalidateByTag")
	}
}

// Членство ключей, которых уже нет, копится только до WithMaxKeys: дальше проход его вычищает —
// тег, который никто не инвалидирует, не растёт без предела (KSEC-06).
func TestMemoryCache_DeadTagMembershipIsBounded(t *testing.T) {
	ctx := context.Background()
	const maxKeys = 200
	m := NewMemoryCache(WithMaxKeys(maxKeys))
	members := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		if set := m.tags["t"]; set != nil {
			return len(set.keys)
		}
		return 0
	}
	write := func(n int) {
		for i := range n {
			key := strconv.Itoa(i)
			if err := m.SetWithTags(ctx, key, []byte("v"), 0, "t"); err != nil {
				t.Fatal(err)
			}
			_ = m.Delete(ctx, key)
		}
	}

	write(maxKeys)
	if got := members(); got != maxKeys {
		t.Fatalf("members under the limit: %d, want every deleted key kept (%d)", got, maxKeys)
	}
	write(20 * maxKeys)
	if got := members(); got > maxKeys+memorySweepMin {
		t.Fatalf("members after %d deleted keys: %d, want <= %d", 21*maxKeys, got, maxKeys+memorySweepMin)
	}
	_ = m.SetWithTags(ctx, "live", []byte("v"), 0, "t")
	write(2 * maxKeys)
	if _, found, _ := m.Get(ctx, "live"); !found {
		t.Fatal("the sweep dropped a live key")
	}
	if err := m.InvalidateByTag(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := m.Get(ctx, "live"); found {
		t.Fatal("the sweep dropped the membership of a live key")
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
	entries, tagSets := len(m.data), len(m.tags)
	m.mu.Unlock()
	if entries > written+memorySweepMin {
		t.Fatalf("entries after sweep: %d, want <= %d", entries, written+memorySweepMin)
	}
	if tagSets != 0 {
		t.Fatalf("tag sets after sweep: %d, want the expired set gone", tagSets)
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

func TestMemoryCache_CompareAndSet(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	m := NewMemoryCache(WithClock(clk))
	_ = m.Set(ctx, "k", []byte("mine"), time.Minute)

	if ok, err := m.CompareAndSet(ctx, "k", []byte("other"), []byte("v"), time.Hour); ok || err != nil {
		t.Fatalf("CompareAndSet on foreign value: ok=%v err=%v", ok, err)
	}
	if v, _, _ := m.Get(ctx, "k"); string(v) != "mine" {
		t.Fatalf("foreign value overwritten: %q", v)
	}
	if ok, err := m.CompareAndSet(ctx, "k", []byte("mine"), []byte("v"), time.Hour); !ok || err != nil {
		t.Fatalf("CompareAndSet on own value: ok=%v err=%v", ok, err)
	}
	clk.Advance(2 * time.Minute)
	if v, found, _ := m.Get(ctx, "k"); !found || string(v) != "v" {
		t.Fatalf("value after CompareAndSet must live by the new ttl: %q %v", v, found)
	}
	if ok, err := m.CompareAndSet(ctx, "missing", []byte("mine"), []byte("v"), time.Hour); ok || err != nil {
		t.Fatalf("CompareAndSet on missing key: ok=%v err=%v", ok, err)
	}
	if _, found, _ := m.Get(ctx, "missing"); found {
		t.Fatal("CompareAndSet created a missing key")
	}
}

// Регрессия KSEC-06: новый ключ сверх WithMaxKeys — ErrMemoryFull, без вытеснения живых;
// перезапись существующего проходит, истёкшие освобождают место.
func TestMemoryCache_MaxKeys(t *testing.T) {
	ctx := context.Background()
	clk := clockkit.NewTickable(time.Unix(1_700_000_000, 0))
	m := NewMemoryCache(WithClock(clk), WithMaxKeys(2))
	_ = m.Set(ctx, "a", []byte("1"), time.Minute)
	_ = m.Set(ctx, "b", []byte("2"), 0)

	full := map[string]func() error{
		"Set":         func() error { return m.Set(ctx, "c", []byte("3"), time.Minute) },
		"SetNX":       func() error { _, err := m.SetNX(ctx, "c", []byte("3"), time.Minute); return err },
		"SetWithTags": func() error { return m.SetWithTags(ctx, "c", []byte("3"), time.Minute, "t") },
	}
	for name, op := range full {
		if err := op(); !errors.Is(err, ErrMemoryFull) {
			t.Errorf("%s of a new key into a full cache: %v, want ErrMemoryFull", name, err)
		}
	}
	if _, found, _ := m.Get(ctx, "c"); found {
		t.Fatal("a rejected key was stored")
	}
	for _, k := range []string{"a", "b"} {
		if _, found, _ := m.Get(ctx, k); !found {
			t.Fatalf("live key %q evicted", k)
		}
	}
	if err := m.Set(ctx, "a", []byte("1b"), time.Minute); err != nil {
		t.Fatalf("overwrite in a full cache: %v", err)
	}

	clk.Advance(time.Minute + memoryFullSweepEvery)
	if err := m.Set(ctx, "c", []byte("3"), time.Minute); err != nil {
		t.Fatalf("Set after a key expired: %v", err)
	}
}

func TestMemoryCache_MaxKeysDefault(t *testing.T) {
	for name, m := range map[string]*MemoryCache{"no option": NewMemoryCache(), "zero": NewMemoryCache(WithMaxKeys(0))} {
		if m.maxKeys != 100_000 {
			t.Errorf("%s: maxKeys = %d, want the documented 100 000", name, m.maxKeys)
		}
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

// Регрессия KAPI-05: nil-опция игнорируется, как у остальных китов, а не роняет кэш на первом Set.
func TestMemoryCache_NilClockIgnored(t *testing.T) {
	m := NewMemoryCache(WithClock(nil))
	if err := m.Set(context.Background(), "k", []byte("v"), time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok, err := m.Get(context.Background(), "k"); err != nil || !ok || string(v) != "v" {
		t.Fatalf("Get = %q, %v, %v", v, ok, err)
	}
}
