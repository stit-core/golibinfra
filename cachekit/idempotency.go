package cachekit

import (
	"bytes"
	"context"
	"time"
)

const idempotencyPrefix = "idempotency:"

// reservedMarker занимает ключ на время выполнения запроса; Get такой ключ
// не отдаёт, чтобы httpkit различал «выполняется» и «готовый ответ».
var reservedMarker = []byte("\x00reserved")

// IdempotencyStore — байтовый store для httpkit.Idempotency поверх Cache:
// ключи в namespace `idempotency:`, резерв через SetNX.
type IdempotencyStore struct {
	c Cache
}

func NewIdempotencyStore(c Cache) *IdempotencyStore {
	return &IdempotencyStore{c: c}
}

func (s *IdempotencyStore) Reserve(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.c.SetNX(ctx, idempotencyPrefix+key, reservedMarker, ttl)
}

func (s *IdempotencyStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, found, err := s.c.Get(ctx, idempotencyPrefix+key)
	if err != nil || !found || bytes.Equal(v, reservedMarker) {
		return nil, false, err
	}
	return v, true, nil
}

func (s *IdempotencyStore) Set(ctx context.Context, key string, record []byte, ttl time.Duration) error {
	return s.c.Set(ctx, idempotencyPrefix+key, record, ttl)
}

func (s *IdempotencyStore) Release(ctx context.Context, key string) error {
	return s.c.Delete(ctx, idempotencyPrefix+key)
}
