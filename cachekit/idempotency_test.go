package cachekit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// Store обязан подходить httpkit.Idempotency без адаптера. Сам кит httpkit не
// импортирует (направление зависимостей), поэтому контракт сторожит тест.
var _ httpkit.IdempotencyStore = (*cachekit.IdempotencyStore)(nil)

func TestIdempotencyStore_ReserveGetSetRelease(t *testing.T) {
	ctx := context.Background()
	store := cachekit.NewIdempotencyStore(cachekit.NewMemoryCache())

	token, ok, err := store.Reserve(ctx, "k", time.Minute)
	if err != nil || !ok || token == "" {
		t.Fatalf("first reserve: token=%q ok=%v err=%v", token, ok, err)
	}
	if _, ok, _ := store.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("second reserve must fail while in-flight")
	}
	if _, found, _ := store.Get(ctx, "k"); found {
		t.Fatal("reserved key must not be visible as a record")
	}

	// Резерв снимает только владелец токена: опоздавший Release чужой не трогает.
	if err := store.Release(ctx, "k", "someone-else"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("Release with a foreign token must keep the reservation")
	}
	if err := store.Release(ctx, "k", token); err != nil {
		t.Fatal(err)
	}
	next, ok, _ := store.Reserve(ctx, "k", time.Minute)
	if !ok {
		t.Fatal("reserve after release must succeed")
	}
	if next == token {
		t.Fatal("each reservation must get its own token")
	}

	if err := store.Set(ctx, "k", []byte(`{"status":201}`), time.Hour); err != nil {
		t.Fatal(err)
	}
	rec, found, err := store.Get(ctx, "k")
	if err != nil || !found || string(rec) != `{"status":201}` {
		t.Fatalf("get after set: %q %v %v", rec, found, err)
	}

	// Release опоздавшего запроса видит уже готовую запись — она не его резерв.
	if err := store.Release(ctx, "k", next); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, "k"); !found {
		t.Fatal("Release must not delete a completed record")
	}
}
