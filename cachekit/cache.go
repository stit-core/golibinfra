// Package cachekit — Redis-обёртка: Cache (байтовый KV с TTL, атомарные
// SetNX/GetDel, тегированная инвалидация), типизированные GetJSON[T]/SetJSON[T],
// IdempotencyStore для httpkit и health-проба. Эмитит cache_commands_total и
// cache_command_duration_seconds.
package cachekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"git.stit.tech/stit-core/golibinfra/healthkit"
)

type Config struct {
	URL          string        `envconfig:"REDIS_URL" default:"redis://localhost:6379/0"`
	PoolSize     int           `envconfig:"REDIS_POOL_SIZE" default:"10"`
	DialTimeout  time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
	ReadTimeout  time.Duration `envconfig:"REDIS_READ_TIMEOUT" default:"3s"`
	WriteTimeout time.Duration `envconfig:"REDIS_WRITE_TIMEOUT" default:"3s"`
	PingOnStart  bool          `envconfig:"REDIS_PING_ON_START" default:"true"`
}

// Cache — байтовый KV. Промах везде — found == false, err == nil.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// SetNX пишет только отсутствующий ключ: атомарный резерв/lock.
	SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	// GetDel читает и удаляет атомарно: одноразовые токены и state.
	GetDel(ctx context.Context, key string) ([]byte, bool, error)
	Delete(ctx context.Context, keys ...string) error
	// SetWithTags пишет ключ и добавляет его в наборы tag:<tag> для InvalidateByTag.
	SetWithTags(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error
	InvalidateByTag(ctx context.Context, tag string) error
	Ping(ctx context.Context) error
	Close() error
}

const tagPrefix = "tag:"

type redisCache struct {
	client *redis.Client
}

func NewRedisCache(cfg Config) (Cache, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("cachekit: parse redis url: %w", err)
	}
	opts.PoolSize = cfg.PoolSize
	opts.DialTimeout = cfg.DialTimeout
	opts.ReadTimeout = cfg.ReadTimeout
	opts.WriteTimeout = cfg.WriteTimeout

	client := redis.NewClient(opts)
	client.AddHook(newMetricsHook())
	return &redisCache{client: client}, nil
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cachekit: get: %w", err)
	}
	return val, true, nil
}

func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cachekit: set: %w", err)
	}
	return nil
}

func (c *redisCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	ok, err := c.client.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("cachekit: setnx: %w", err)
	}
	return ok, nil
}

func (c *redisCache) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.client.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cachekit: getdel: %w", err)
	}
	return val, true, nil
}

func (c *redisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cachekit: delete: %w", err)
	}
	return nil
}

func (c *redisCache) SetWithTags(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	pipe := c.client.TxPipeline()
	pipe.Set(ctx, key, value, ttl)
	for _, tag := range tags {
		tagKey := tagPrefix + tag
		pipe.SAdd(ctx, tagKey, key)
		if ttl > 0 {
			// Набор живёт ttl после последней записи с этим тегом.
			pipe.Expire(ctx, tagKey, ttl)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("cachekit: set with tags: %w", err)
	}
	return nil
}

func (c *redisCache) InvalidateByTag(ctx context.Context, tag string) error {
	tagKey := tagPrefix + tag
	keys, err := c.client.SMembers(ctx, tagKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("cachekit: invalidate tag members: %w", err)
	}
	if err := c.client.Del(ctx, append(keys, tagKey)...).Err(); err != nil {
		return fmt.Errorf("cachekit: invalidate delete: %w", err)
	}
	return nil
}

func (c *redisCache) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cachekit: ping: %w", err)
	}
	return nil
}

func (c *redisCache) Close() error {
	if err := c.client.Close(); err != nil {
		return fmt.Errorf("cachekit: close: %w", err)
	}
	return nil
}

func Probe(c Cache) healthkit.Probe {
	return c.Ping
}

func GetJSON[T any](ctx context.Context, c Cache, key string) (T, bool, error) {
	var zero T
	data, found, err := c.Get(ctx, key)
	if err != nil || !found {
		return zero, found, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, false, fmt.Errorf("cachekit: unmarshal: %w", err)
	}
	return v, true, nil
}

func SetJSON[T any](ctx context.Context, c Cache, key string, value T, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cachekit: marshal: %w", err)
	}
	return c.Set(ctx, key, data, ttl)
}
