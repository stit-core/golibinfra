package testkit_test

import (
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestStartRedis(t *testing.T) {
	fix := testkit.StartRedis(t)
	if !strings.HasPrefix(fix.URL, "redis://") || fix.Container == nil {
		t.Fatalf("fixture = %+v", fix)
	}

	opts, err := redis.ParseURL(fix.URL)
	if err != nil {
		t.Fatalf("parse url %q: %v", fix.URL, err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := client.Set(t.Context(), "k", "v", 0).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := client.Get(t.Context(), "k").Result()
	if err != nil || got != "v" {
		t.Fatalf("get = %q, err = %v", got, err)
	}
}
