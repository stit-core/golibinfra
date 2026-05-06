package cachekit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type idempotencyEntry struct {
	Response []byte `json:"response"`
	Status   int    `json:"status"`
}

type idempotencyStore struct {
	cache Cache
	ttl   time.Duration
}

func IdempotencyStore(cache Cache, ttl time.Duration) interface {
	Get(ctx context.Context, key string) ([]byte, int, bool, error)
	Set(ctx context.Context, key string, response []byte, status int) error
} {
	return &idempotencyStore{cache: cache, ttl: ttl}
}

func (s *idempotencyStore) Get(ctx context.Context, key string) ([]byte, int, bool, error) {
	data, found, err := s.cache.Get(ctx, "idempotency:"+key)
	if err != nil || !found {
		return nil, 0, found, err
	}
	var entry idempotencyEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, 0, false, fmt.Errorf("cachekit: idempotency unmarshal: %w", err)
	}
	return entry.Response, entry.Status, true, nil
}

func (s *idempotencyStore) Set(ctx context.Context, key string, response []byte, status int) error {
	data, err := json.Marshal(idempotencyEntry{Response: response, Status: status})
	if err != nil {
		return fmt.Errorf("cachekit: idempotency marshal: %w", err)
	}
	return s.cache.Set(ctx, "idempotency:"+key, data, s.ttl)
}
