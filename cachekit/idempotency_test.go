package cachekit_test

import (
	"context"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

func TestIdempotencyStore_ReserveGetSetRelease(t *testing.T) {
	ctx := context.Background()
	store := cachekit.NewIdempotencyStore(cachekit.NewMemoryCache())

	ok, err := store.Reserve(ctx, "k", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first reserve: ok=%v err=%v", ok, err)
	}
	if ok, _ := store.Reserve(ctx, "k", time.Minute); ok {
		t.Fatal("second reserve must fail while in-flight")
	}
	if _, found, _ := store.Get(ctx, "k"); found {
		t.Fatal("reserved key must not be visible as a record")
	}

	if err := store.Set(ctx, "k", []byte(`{"status":201}`), time.Hour); err != nil {
		t.Fatal(err)
	}
	rec, found, err := store.Get(ctx, "k")
	if err != nil || !found || string(rec) != `{"status":201}` {
		t.Fatalf("get after set: %q %v %v", rec, found, err)
	}

	if err := store.Release(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Reserve(ctx, "k", time.Minute); !ok {
		t.Fatal("reserve after release must succeed")
	}
}
