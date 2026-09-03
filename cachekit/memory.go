package cachekit

import (
	"context"
	"sync"
	"time"
)

// MemoryCache — in-memory реализация Cache для тестов потребителя: та же
// семантика TTL, SetNX, GetDel и тегов без Redis.
type MemoryCache struct {
	mu   sync.Mutex
	now  func() time.Time
	data map[string]memEntry
	tags map[string]map[string]struct{}
}

type memEntry struct {
	value     []byte
	expiresAt time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{now: time.Now, data: map[string]memEntry{}, tags: map[string]map[string]struct{}{}}
}

func (m *MemoryCache) live(key string) ([]byte, bool) {
	e, ok := m.data[key]
	if !ok {
		return nil, false
	}
	if !e.expiresAt.IsZero() && !m.now().Before(e.expiresAt) {
		delete(m.data, key)
		return nil, false
	}
	return e.value, true
}

func (m *MemoryCache) put(key string, value []byte, ttl time.Duration) {
	e := memEntry{value: append([]byte(nil), value...)}
	if ttl > 0 {
		e.expiresAt = m.now().Add(ttl)
	}
	m.data[key] = e
}

func (m *MemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	return v, ok, nil
}

func (m *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(key, value, ttl)
	return nil
}

func (m *MemoryCache) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return false, nil
	}
	m.put(key, value, ttl)
	return true, nil
}

func (m *MemoryCache) GetDel(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	delete(m.data, key)
	return v, ok, nil
}

func (m *MemoryCache) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}

func (m *MemoryCache) SetWithTags(_ context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(key, value, ttl)
	for _, tag := range tags {
		set, ok := m.tags[tag]
		if !ok {
			set = map[string]struct{}{}
			m.tags[tag] = set
		}
		set[key] = struct{}{}
	}
	return nil
}

func (m *MemoryCache) InvalidateByTag(_ context.Context, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.tags[tag] {
		delete(m.data, k)
	}
	delete(m.tags, tag)
	return nil
}

func (m *MemoryCache) Ping(context.Context) error { return nil }

func (m *MemoryCache) Close() error { return nil }
