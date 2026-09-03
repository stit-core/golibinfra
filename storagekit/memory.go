package storagekit

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// MemoryStorage — in-memory Storage для тестов и локального dev.
// Не персистентен; ключи, URL и контракт size совпадают с боевым адаптером.
type MemoryStorage struct {
	urls *URLBuilder

	mu   sync.RWMutex
	objs map[string][]byte
}

func NewMemoryStorage(urls *URLBuilder) *MemoryStorage {
	return &MemoryStorage{urls: urls, objs: make(map[string][]byte)}
}

func (m *MemoryStorage) Put(_ context.Context, r io.Reader, contentType string, size int64) (Ref, error) {
	if size >= 0 {
		r = io.LimitReader(r, size+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: read body: %w", ErrUploadFailed, err)
	}
	if size >= 0 && int64(len(data)) != size {
		return Ref{}, fmt.Errorf("%w: body length differs from declared %d", ErrUploadFailed, size)
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
	delete(m.objs, key)
	return nil
}

// PresignedGet — test-double: детерминированная псевдо-подписанная ссылка
// (bucket игнорируется, схема ключа совпадает с боевым URLBuilder).
func (m *MemoryStorage) PresignedGet(_ context.Context, _, key string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", fmt.Errorf("%w: key required", ErrPresignFailed)
	}
	return fmt.Sprintf("%s?x-presigned-ttl=%d", m.urls.Build(key), int64(ttl.Seconds())), nil
}

// Get — тест-хелпер: вернуть сохранённые байты.
func (m *MemoryStorage) Get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.objs[key]
	return b, ok
}
