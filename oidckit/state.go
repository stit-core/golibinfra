package oidckit

import (
	"context"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

// StateStore хранит короткоживущий state логин-флоу (PKCE verifier / nonce /
// return-to) между /auth/login и /auth/callback. Записи one-time: GetDel отдаёт
// значение и сразу удаляет его (повторный callback с тем же state отвергается).
type StateStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	GetDel(ctx context.Context, key string) ([]byte, bool, error)
}

// NewCacheStateStore — StateStore поверх cachekit.Cache: Redis в проде,
// cachekit.NewMemoryCache() для тестов и single-replica (state — в процессе).
func NewCacheStateStore(c cachekit.Cache) StateStore {
	return &cacheStateStore{c: c}
}

type cacheStateStore struct {
	c cachekit.Cache
}

func (s *cacheStateStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.c.Set(ctx, key, value, ttl)
}

func (s *cacheStateStore) GetDel(ctx context.Context, key string) ([]byte, bool, error) {
	return s.c.GetDel(ctx, key)
}
