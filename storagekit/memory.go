package storagekit

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// MemoryStorage — in-memory Storage для тестов и локального dev.
// Не персистентен; ключи и URL совпадают по схеме с боевым адаптером.
type MemoryStorage struct {
	urls *URLBuilder

	mu   sync.RWMutex
	objs map[string][]byte
}

func NewMemoryStorage(urls *URLBuilder) *MemoryStorage {
	return &MemoryStorage{urls: urls, objs: make(map[string][]byte)}
}

func (m *MemoryStorage) Put(_ context.Context, r io.Reader, contentType string, size int64) (Ref, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return Ref{}, fmt.Errorf("%w: read body: %w", ErrUploadFailed, err)
	}
	if int64(len(data)) > size {
		return Ref{}, fmt.Errorf("%w: body larger than declared %d", ErrUploadFailed, size)
	}

	key, err := NewKey(contentType)
	if err != nil {
		return Ref{}, err
	}

	m.mu.Lock()
	m.objs[key] = data
	m.mu.Unlock()

	return Ref{
		Key:         key,
		URL:         m.urls.Build(key),
		ContentType: contentType,
		Size:        int64(len(data)),
	}, nil
}

func (m *MemoryStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	delete(m.objs, key)
	return nil
}

// Get — тест-хелпер: вернуть сохранённые байты.
func (m *MemoryStorage) Get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.objs[key]
	return b, ok
}
