// Package cachekit — Redis-обёртка: интерфейс Cache, типизированные
// helper'ы GetJSON[T]/SetJSON[T], tag-based invalidation
// (InvalidateByTag) и адаптер IdempotencyStore для httpkit.Idempotency.
package cachekit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	URL          string        `envconfig:"REDIS_URL" default:"redis://localhost:6379/0"`
	PoolSize     int           `envconfig:"REDIS_POOL_SIZE" default:"10"`
	DialTimeout  time.Duration `envconfig:"REDIS_DIAL_TIMEOUT" default:"5s"`
	ReadTimeout  time.Duration `envconfig:"REDIS_READ_TIMEOUT" default:"3s"`
	WriteTimeout time.Duration `envconfig:"REDIS_WRITE_TIMEOUT" default:"3s"`
}

type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	InvalidateByTag(ctx context.Context, tag string) error
	Ping(ctx context.Context) error
}

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

func (c *redisCache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cachekit: delete: %w", err)
	}
	return nil
}

func (c *redisCache) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cachekit: ping: %w", err)
	}
	return nil
}

func Probe(c Cache) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return c.Ping(ctx)
	}
}

func (c *redisCache) InvalidateByTag(ctx context.Context, tag string) error {
	tagKey := "tag:" + tag
	keys, err := c.client.SMembers(ctx, tagKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("cachekit: invalidate tag members: %w", err)
	}
	if len(keys) > 0 {
		if err := c.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("cachekit: invalidate delete keys: %w", err)
		}
	}
	if err := c.client.Del(ctx, tagKey).Err(); err != nil {
		return fmt.Errorf("cachekit: invalidate delete tag: %w", err)
	}
	return nil
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
