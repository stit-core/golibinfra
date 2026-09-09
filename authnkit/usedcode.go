package authnkit

import (
	"context"
	"sync"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// MemoryUsedCodeStore — UsedCodeStore в памяти процесса: годится для одной
// реплики. Нескольким репликам нужен общий store (cachekit.Cache.SetNX).
type MemoryUsedCodeStore struct {
	mu    sync.Mutex
	clock clockkit.Clock
	until map[string]time.Time
}

// NewMemoryUsedCodeStore — пустой store; часы подменяются WithClock.
func NewMemoryUsedCodeStore(opts ...UsedCodeStoreOption) *MemoryUsedCodeStore {
	o := usedCodeStoreOptions(opts)
	return &MemoryUsedCodeStore{clock: o.clock, until: map[string]time.Time{}}
}

// MarkUsed — false, если ключ уже отмечен и его ttl не истёк.
func (s *MemoryUsedCodeStore) MarkUsed(_ context.Context, key string, ttl time.Duration) (bool, error) {
	now := s.clock.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Записи живут единицы минут, а чистить их некому: подметаем на каждой
	// записи, иначе store растёт вместе с числом логинов.
	for k, exp := range s.until {
		if !exp.After(now) {
			delete(s.until, k)
		}
	}
	if exp, ok := s.until[key]; ok && exp.After(now) {
		return false, nil
	}
	s.until[key] = now.Add(ttl)
	return true, nil
}
