package cachekit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// memorySweepMin — нижняя граница числа записей между полными проходами: у
// маленького кэша проход дёшев, дальше порог растёт вместе с числом ключей.
const memorySweepMin = 64

// memoryMaxKeys — предел числа ключей MemoryCache по умолчанию (WithMaxKeys).
const memoryMaxKeys = 100_000

// memoryFullSweepEvery — у полного кэша полный проход не чаще раза в секунду: поток новых ключей
// не платит O(n) за каждую отвергнутую запись.
const memoryFullSweepEvery = time.Second

// ErrMemoryFull — в MemoryCache предел ключей (WithMaxKeys): новый ключ не пишется. Ключи не
// вытесняются — резерв идемпотентности или lock не пропадает молча.
var ErrMemoryFull = errors.New("cachekit: memory cache is full")

// MemoryCache — in-memory реализация Cache: та же семантика TTL, SetNX, GetDel и
// тегов без Redis. Пригодна и для single-replica развёртывания (истёкшие ключи
// и членство в тегах вычищаются амортизированным проходом на записи); число ключей
// ограничено (WithMaxKeys).
type MemoryCache struct {
	mu               sync.Mutex
	clock            clockkit.Clock
	maxKeys          int
	data             map[string]memEntry
	tags             map[string]map[string]struct{}
	writesSinceSweep int
	sizeAtSweep      int
	lastFullSweep    time.Time
}

type memEntry struct {
	value     []byte
	expiresAt time.Time
}

// MemoryOption — опция NewMemoryCache.
type MemoryOption func(*MemoryCache)

// WithClock подменяет часы кэша: тест потребителя двигает TTL без sleep; nil — часы не
// меняются.
func WithClock(clk clockkit.Clock) MemoryOption {
	return func(m *MemoryCache) {
		if clk != nil {
			m.clock = clk
		}
	}
}

// WithMaxKeys задаёт предел числа ключей (по умолчанию 100 000): новый ключ сверх него —
// ErrMemoryFull. Предел — ключи, не байты: под крупные значения его ставят меньше. n < 1 ничего
// не меняет.
func WithMaxKeys(n int) MemoryOption {
	return func(m *MemoryCache) {
		if n >= 1 {
			m.maxKeys = n
		}
	}
}

// NewMemoryCache собирает потокобезопасный in-memory Cache.
func NewMemoryCache(opts ...MemoryOption) *MemoryCache {
	m := &MemoryCache{
		clock:   clockkit.Real{},
		maxKeys: memoryMaxKeys,
		data:    map[string]memEntry{},
		tags:    map[string]map[string]struct{}{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *MemoryCache) live(key string) ([]byte, bool) {
	e, ok := m.data[key]
	if !ok {
		return nil, false
	}
	if !e.expiresAt.IsZero() && !m.clock.Now().Before(e.expiresAt) {
		m.drop(key)
		return nil, false
	}
	return e.value, true
}

// put пишет ключ; новый ключ в полный кэш — после прохода по истёкшим, иначе ErrMemoryFull.
func (m *MemoryCache) put(key string, value []byte, ttl time.Duration) error {
	if _, exists := m.data[key]; !exists && len(m.data) >= m.maxKeys {
		if now := m.clock.Now(); now.Sub(m.lastFullSweep) >= memoryFullSweepEvery {
			m.lastFullSweep = now
			m.sweep()
		}
		if len(m.data) >= m.maxKeys {
			return fmt.Errorf("%w: %d keys", ErrMemoryFull, len(m.data))
		}
	}
	e := memEntry{value: bytes.Clone(value)}
	if ttl > 0 {
		e.expiresAt = m.clock.Now().Add(ttl)
	}
	m.data[key] = e
	m.sweepIfDue()
	return nil
}

func (m *MemoryCache) drop(key string) {
	delete(m.data, key)
	for tag, set := range m.tags {
		delete(set, key)
		if len(set) == 0 {
			delete(m.tags, tag)
		}
	}
}

// sweepIfDue амортизирует полный проход: он идёт, когда с прошлого прохода
// сделано не меньше записей, чем было ключей, — O(1) на запись, а мёртвых
// ключей и членства в тегах не накапливается больше, чем живых.
func (m *MemoryCache) sweepIfDue() {
	m.writesSinceSweep++
	threshold := max(m.sizeAtSweep, memorySweepMin)
	if m.writesSinceSweep < threshold {
		return
	}
	m.sweep()
}

// sweep — полный проход: удаляет истёкшие ключи и их членство в тегах.
func (m *MemoryCache) sweep() {
	m.writesSinceSweep = 0
	now := m.clock.Now()
	for k, e := range m.data {
		if !e.expiresAt.IsZero() && !now.Before(e.expiresAt) {
			delete(m.data, k)
		}
	}
	for tag, set := range m.tags {
		for k := range set {
			if _, ok := m.data[k]; !ok {
				delete(set, k)
			}
		}
		if len(set) == 0 {
			delete(m.tags, tag)
		}
	}
	m.sizeAtSweep = len(m.data)
}

// Get возвращает копию значения: правка полученных байт не меняет кэш.
func (m *MemoryCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	if !ok {
		return nil, false, nil
	}
	return bytes.Clone(v), true, nil
}

// Set пишет значение с TTL (0 — без срока жизни).
func (m *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.put(key, value, ttl)
}

// SetNX пишет значение, только если живого ключа нет.
func (m *MemoryCache) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return false, nil
	}
	if err := m.put(key, value, ttl); err != nil {
		return false, err
	}
	return true, nil
}

// GetDel возвращает копию значения и удаляет ключ.
func (m *MemoryCache) GetDel(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	if !ok {
		return nil, false, nil
	}
	v = bytes.Clone(v)
	m.drop(key)
	return v, true, nil
}

// CompareAndDelete удаляет ключ, только если его значение равно expected.
func (m *MemoryCache) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	if !ok || !bytes.Equal(v, expected) {
		return false, nil
	}
	m.drop(key)
	return true, nil
}

// CompareAndSet пишет значение с TTL, только если значение ключа равно expected.
func (m *MemoryCache) CompareAndSet(_ context.Context, key string, expected, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	if !ok || !bytes.Equal(v, expected) {
		return false, nil
	}
	return true, m.put(key, value, ttl)
}

// Delete удаляет ключи вместе с их членством в тегах.
func (m *MemoryCache) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.drop(k)
	}
	return nil
}

// SetWithTags пишет значение и добавляет ключ в наборы тегов.
func (m *MemoryCache) SetWithTags(_ context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.put(key, value, ttl); err != nil {
		return err
	}
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

// InvalidateByTag удаляет все ключи тега и сам набор; отсутствующий тег — не ошибка.
func (m *MemoryCache) InvalidateByTag(_ context.Context, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.tags[tag] {
		delete(m.data, k)
	}
	delete(m.tags, tag)
	return nil
}

// Ping всегда успешен: внешней зависимости у кэша нет.
func (m *MemoryCache) Ping(context.Context) error { return nil }

// Close — no-op: закрывать нечего, повторный вызов тоже возвращает nil.
func (m *MemoryCache) Close() error { return nil }
