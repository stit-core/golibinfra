package cachekit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestCheckInfoVersion(t *testing.T) {
	tests := []struct {
		name    string
		info    string
		wantErr bool
	}{
		{"redis 7", "# Server\r\nredis_version:7.2.4\r\nos:Linux\r\n", false},
		{"redis 8", "redis_version:8.0.0\n", false},
		{"valkey reporting compat version", "redis_version:7.2.4\nvalkey_version:8.0.1\n", false},
		{"redis 6", "# Server\r\nredis_version:6.2.24\r\n", true},
		{"redis 5", "redis_version:5.0.14\n", true},
		{"no version field", "# Server\r\nos:Linux\r\n", true},
		{"unparsable version", "redis_version:experimental\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkInfoVersion(tt.info)
			if tt.wantErr && !errors.Is(err, ErrServerVersion) {
				t.Fatalf("checkInfoVersion = %v, want ErrServerVersion", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("checkInfoVersion = %v, want nil", err)
			}
		})
	}
	if got := infoField("# Server\r\nredis_version:7.2.4\r\n", "redis_version"); got != "7.2.4" {
		t.Fatalf("infoField = %q", got)
	}
}

// failCmdHook отвергает команду по имени, не отправляя её на сервер.
type failCmdHook struct {
	cmd string
	err error
}

func (failCmdHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h failCmdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == h.cmd {
			return h.err
		}
		return next(ctx, cmd)
	}
}

func (failCmdHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Снятие членства и удаление ключей — одна серверная операция: отдельный UNLINK,
// упавший по сети, оставил бы ключи в Redis вне какого-либо тега, и повторная
// инвалидация их бы уже не нашла.
func TestRedisCache_InvalidateByTagIsOneCommand(t *testing.T) {
	url := testkit.StartRedis(t).URL
	c, err := newRedisCache(Config{
		URL: url, PoolSize: 5, MaxRetries: -1,
		DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.client.AddHook(failCmdHook{cmd: "unlink", err: errors.New("network is down")})

	ctx := context.Background()
	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := c.SetWithTags(ctx, k, []byte("v"), time.Minute, "t"); err != nil {
			t.Fatalf("set with tags: %v", err)
		}
	}

	if err := c.InvalidateByTag(ctx, "t"); err != nil {
		t.Fatalf("InvalidateByTag: %v", err)
	}
	for _, k := range keys {
		if _, found, err := c.Get(ctx, k); err != nil || found {
			t.Fatalf("key %q survived invalidation: found=%v err=%v", k, found, err)
		}
	}
	if n, err := c.client.Exists(ctx, "tag:t").Result(); err != nil || n != 0 {
		t.Fatalf("tag set after invalidation: exists=%d err=%v", n, err)
	}
}

// Недоступный сервер — ошибка проверки, а не молчаливое «версия неизвестна».
func TestCheckServerVersion_PropagatesClientError(t *testing.T) {
	c, err := newRedisCache(Config{
		URL: "redis://127.0.0.1:6379/0", PoolSize: 1, MaxRetries: -1,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("newRedisCache: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.checkServerVersion(context.Background()); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("checkServerVersion on a closed client = %v, want a wrapped redis.ErrClosed", err)
	}
}
