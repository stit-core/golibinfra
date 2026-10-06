package cachekit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/testkit"
	"git.stit.tech/stit-core/golibinfra/testkit/idempotencytest"
)

type redisFixture struct {
	cache cachekit.Cache
	raw   *redis.Client
	url   string
}

// startRedis поднимает контейнер и отдаёт кэш вместе с сырым клиентом: TTL и
// раскладку служебных ключей проверяем в Redis, а не через API кита.
func startRedis(t *testing.T, prefix string) redisFixture {
	t.Helper()
	url := testkit.StartRedis(t).URL
	return redisFixture{cache: newCache(t, url, prefix), raw: rawClient(t, url), url: url}
}

func newCache(t *testing.T, url, prefix string) cachekit.Cache {
	t.Helper()
	cfg := validConfig()
	cfg.URL = url
	cfg.KeyPrefix = prefix
	cfg.PoolSize = 5
	cache, err := cachekit.NewRedisCache(cfg)
	if err != nil {
		t.Fatalf("new redis cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func rawClient(t *testing.T, url string) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRedisCache(t *testing.T) {
	fix := startRedis(t, "")
	cache, raw := fix.cache, fix.raw
	ctx := context.Background()

	t.Run("get set miss", func(t *testing.T) {
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
	})

	t.Run("delete", func(t *testing.T) {
		_ = cache.Set(ctx, "k1", []byte("v1"), time.Minute)
		if err := cache.Delete(ctx, "k1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, found, _ := cache.Get(ctx, "k1"); found {
			t.Error("expected key to be deleted")
		}
	})

	t.Run("setnx and getdel", func(t *testing.T) {
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
	})

	t.Run("compare and delete", func(t *testing.T) {
		_ = cache.Set(ctx, "cad", []byte("mine"), time.Minute)
		if ok, err := cache.CompareAndDelete(ctx, "cad", []byte("theirs")); ok || err != nil {
			t.Fatalf("foreign value: ok=%v err=%v", ok, err)
		}
		if _, found, _ := cache.Get(ctx, "cad"); !found {
			t.Fatal("foreign value must survive")
		}
		if ok, err := cache.CompareAndDelete(ctx, "cad", []byte("mine")); !ok || err != nil {
			t.Fatalf("own value: ok=%v err=%v", ok, err)
		}
		if _, found, _ := cache.Get(ctx, "cad"); found {
			t.Fatal("own value must be deleted")
		}
		if ok, err := cache.CompareAndDelete(ctx, "cad", []byte("mine")); ok || err != nil {
			t.Fatalf("missing key: ok=%v err=%v", ok, err)
		}
	})

	t.Run("compare and set", func(t *testing.T) {
		_ = cache.Set(ctx, "cas", []byte("mine"), time.Minute)
		if ok, err := cache.CompareAndSet(ctx, "cas", []byte("theirs"), []byte("v"), time.Hour); ok || err != nil {
			t.Fatalf("foreign value: ok=%v err=%v", ok, err)
		}
		if v, _, _ := cache.Get(ctx, "cas"); string(v) != "mine" {
			t.Fatalf("foreign value overwritten: %q", v)
		}
		if ok, err := cache.CompareAndSet(ctx, "cas", []byte("mine"), []byte("v"), time.Hour); !ok || err != nil {
			t.Fatalf("own value: ok=%v err=%v", ok, err)
		}
		if v, _, _ := cache.Get(ctx, "cas"); string(v) != "v" {
			t.Fatalf("own value not replaced: %q", v)
		}
		if ttl := raw.TTL(ctx, "cas").Val(); ttl <= time.Minute {
			t.Fatalf("TTL after CompareAndSet: %s, want the new ttl (1h)", ttl)
		}
		if ok, err := cache.CompareAndSet(ctx, "cas:missing", []byte("mine"), []byte("v"), time.Hour); ok || err != nil {
			t.Fatalf("missing key: ok=%v err=%v", ok, err)
		}
		if n := raw.Exists(ctx, "cas:missing").Val(); n != 0 {
			t.Fatal("CompareAndSet created a missing key")
		}
	})

	t.Run("ttl is applied and expires", func(t *testing.T) {
		_ = cache.Set(ctx, "ttl:minute", []byte("v"), time.Minute)
		if ttl := raw.TTL(ctx, "ttl:minute").Val(); ttl <= 0 {
			t.Errorf("TTL after Set: %s, want > 0", ttl)
		}
		_ = cache.SetWithTags(ctx, "ttl:tagged", []byte("v"), time.Minute, "ttl")
		if ttl := raw.TTL(ctx, "ttl:tagged").Val(); ttl <= 0 {
			t.Errorf("TTL after SetWithTags: %s, want > 0", ttl)
		}
		_ = cache.Set(ctx, "ttl:forever", []byte("v"), 0)
		if ttl := raw.TTL(ctx, "ttl:forever").Val(); ttl != -1 {
			t.Errorf("TTL of a key without ttl: %s, want -1 (persistent)", ttl)
		}

		// Субмиллисекундный TTL не должен превратиться в «без срока жизни».
		_ = cache.SetWithTags(ctx, "ttl:sub-ms", []byte("v"), 100*time.Microsecond, "ttl")
		if ttl := raw.TTL(ctx, "ttl:sub-ms").Val(); ttl == -1 {
			t.Error("TTL of a sub-millisecond key: -1, want an expiry")
		}

		_ = cache.Set(ctx, "ttl:short", []byte("v"), 50*time.Millisecond)
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, found, err := cache.Get(ctx, "ttl:short")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("key with a 50ms TTL is still alive")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("json round-trip", func(t *testing.T) {
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
	})

	t.Run("probe", func(t *testing.T) {
		if err := cachekit.Probe(cache).Run(ctx); err != nil {
			t.Fatalf("probe: %v", err)
		}
	})
}

func TestRedisCache_Tags(t *testing.T) {
	fix := startRedis(t, "")
	cache, raw := fix.cache, fix.raw
	ctx := context.Background()

	t.Run("invalidate by tag", func(t *testing.T) {
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
		if n, _ := raw.Exists(ctx, "tag:users").Result(); n != 0 {
			t.Error("empty tag set must be gone")
		}
		if err := cache.InvalidateByTag(ctx, "missing"); err != nil {
			t.Errorf("missing tag: %v", err)
		}
	})

	// Набор тега — единственный индекс группы: истечь раньше живого члена он не
	// имеет права ни при каком порядке записей.
	t.Run("tag set outlives its longest-lived member", func(t *testing.T) {
		_ = cache.SetWithTags(ctx, "ttl:long-first", []byte("v"), time.Hour, "t-long-first")
		_ = cache.SetWithTags(ctx, "ttl:short-after", []byte("v"), time.Minute, "t-long-first")
		if ttl := raw.TTL(ctx, "tag:t-long-first").Val(); ttl < 30*time.Minute {
			t.Errorf("tag TTL after a shorter member: %s, want ~1h", ttl)
		}

		_ = cache.SetWithTags(ctx, "ttl:short-first", []byte("v"), time.Minute, "t-short-first")
		_ = cache.SetWithTags(ctx, "ttl:long-after", []byte("v"), time.Hour, "t-short-first")
		if ttl := raw.TTL(ctx, "tag:t-short-first").Val(); ttl < 30*time.Minute {
			t.Errorf("tag TTL after a longer member: %s, want ~1h", ttl)
		}

		_ = cache.SetWithTags(ctx, "ttl:volatile", []byte("v"), time.Minute, "t-persistent")
		_ = cache.SetWithTags(ctx, "ttl:persistent", []byte("v"), 0, "t-persistent")
		if ttl := raw.TTL(ctx, "tag:t-persistent").Val(); ttl != -1 {
			t.Errorf("tag TTL after a persistent member: %s, want -1", ttl)
		}
		_ = cache.SetWithTags(ctx, "ttl:volatile-again", []byte("v"), time.Minute, "t-persistent")
		if ttl := raw.TTL(ctx, "tag:t-persistent").Val(); ttl != -1 {
			t.Errorf("tag TTL of a persistent set after a volatile member: %s, want -1", ttl)
		}
	})

	t.Run("large tag set", func(t *testing.T) {
		const n = 2000
		keys := make([]string, n)
		for i := range n {
			keys[i] = fmt.Sprintf("bulk:%d", i)
			if err := cache.SetWithTags(ctx, keys[i], []byte("v"), time.Hour, "bulk"); err != nil {
				t.Fatalf("set with tags: %v", err)
			}
		}
		if err := cache.InvalidateByTag(ctx, "bulk"); err != nil {
			t.Fatalf("invalidate: %v", err)
		}
		if alive, _ := raw.Exists(ctx, keys...).Result(); alive != 0 {
			t.Errorf("keys alive after invalidation: %d, want 0", alive)
		}
		if n, _ := raw.Exists(ctx, "tag:bulk").Result(); n != 0 {
			t.Error("tag set must be gone")
		}
	})

	// Запись, попавшая в тег во время инвалидации, обязана остаться
	// инвалидируемой: иначе кэш «иногда не сбрасывается».
	t.Run("write during invalidation stays invalidatable", func(t *testing.T) {
		const rounds, preset, concurrent = 10, 300, 200
		type result struct {
			keys []string
			err  error
		}
		for round := range rounds {
			keys := make([]string, 0, preset)
			for i := range preset {
				k := fmt.Sprintf("race:%d:pre:%d", round, i)
				if err := cache.SetWithTags(ctx, k, []byte("v"), time.Hour, "race"); err != nil {
					t.Fatalf("set with tags: %v", err)
				}
				keys = append(keys, k)
			}

			done := make(chan result, 1)
			go func() {
				var r result
				for i := range concurrent {
					k := fmt.Sprintf("race:%d:live:%d", round, i)
					if err := cache.SetWithTags(ctx, k, []byte("v"), time.Hour, "race"); err != nil {
						r.err = err
						break
					}
					r.keys = append(r.keys, k)
				}
				done <- r
			}()

			if err := cache.InvalidateByTag(ctx, "race"); err != nil {
				t.Fatalf("invalidate: %v", err)
			}
			written := <-done
			if written.err != nil {
				t.Fatalf("concurrent set with tags: %v", written.err)
			}
			// Ключ, записанный во время инвалидации, либо уже удалён, либо всё
			// ещё член набора: второй проход обязан добить остаток.
			if err := cache.InvalidateByTag(ctx, "race"); err != nil {
				t.Fatalf("second invalidate: %v", err)
			}

			keys = append(keys, written.keys...)
			if alive, _ := raw.Exists(ctx, keys...).Result(); alive != 0 {
				t.Fatalf("round %d: keys alive after invalidation: %d of %d, want 0", round, alive, len(keys))
			}
		}
	})
}

func TestRedisCache_KeyPrefix(t *testing.T) {
	fix := startRedis(t, "svc-a:")
	other := newCache(t, fix.url, "svc-b:")
	raw := fix.raw
	ctx := context.Background()

	if err := fix.cache.Set(ctx, "k", []byte("a"), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n, _ := raw.Exists(ctx, "svc-a:k").Result(); n != 1 {
		t.Fatal("key must be stored under REDIS_KEY_PREFIX")
	}
	if _, found, _ := other.Get(ctx, "k"); found {
		t.Error("another prefix must not see the key")
	}

	_ = fix.cache.SetWithTags(ctx, "tagged", []byte("a"), time.Minute, "t")
	_ = other.SetWithTags(ctx, "tagged", []byte("b"), time.Minute, "t")
	if n, _ := raw.Exists(ctx, "svc-a:tag:t", "svc-b:tag:t").Result(); n != 2 {
		t.Fatal("tag sets must be prefixed per service")
	}
	if err := other.InvalidateByTag(ctx, "t"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, found, _ := fix.cache.Get(ctx, "tagged"); !found {
		t.Error("invalidation must not cross the prefix")
	}

	if _, _, err := cachekit.NewIdempotencyStore(fix.cache).Reserve(ctx, "req-1", time.Minute); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if n, _ := raw.Exists(ctx, "svc-a:idempotency:req-1").Result(); n != 1 {
		t.Error("idempotency keys must be prefixed too")
	}
}

// Redis-реализация проходит тот же контракт, что память и httpkit.MemoryStore (KTST-10): у кейса
// свой префикс ключей на общем сервере.
func TestIdempotencyStore_ContractOnRedis(t *testing.T) {
	url := testkit.StartRedis(t).URL
	idempotencytest.RunContract(t, func(t *testing.T) httpkit.IdempotencyStore {
		return cachekit.NewIdempotencyStore(newCache(t, url, t.Name()+":"))
	})
}
