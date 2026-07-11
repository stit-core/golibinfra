package oidckit

import (
	"context"
	"sync"
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

// NewCacheStateStore — StateStore поверх cachekit.Cache (Redis): переживает
// рестарт приложения и работает при нескольких репликах.
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
	v, ok, err := s.c.Get(ctx, key)
	if err != nil || !ok {
		return nil, false, err
	}
	// Get+Delete не атомарны, но state и так одноразовый секрет с TTL:
	// гонка двух callback'ов с одним state в худшем случае пустит один из них.
	if err := s.c.Delete(ctx, key); err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// NewMemoryStateStore — StateStore в памяти процесса: для тестов и
// single-replica-развёртываний без Redis (state не переживает рестарт).
func NewMemoryStateStore() StateStore {
	return &memoryStateStore{entries: map[string]memoryEntry{}}
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

type memoryStateStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

func (s *memoryStateStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	v := make([]byte, len(value))
	copy(v, value)
	s.entries[key] = memoryEntry{value: v, expiresAt: time.Now().Add(ttl)}
	return nil
}

func (s *memoryStateStore) GetDel(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	delete(s.entries, key)
	if time.Now().After(e.expiresAt) {
		return nil, false, nil
	}
	return e.value, true, nil
}

// purgeLocked лениво выметает протухшие записи на Set — фонового janitor'а нет,
// а брошенные логин-флоу не должны копиться бесконечно.
func (s *memoryStateStore) purgeLocked() {
	now := time.Now()
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
		}
	}
}
