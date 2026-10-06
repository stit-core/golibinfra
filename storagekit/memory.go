package storagekit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// MemoryStorage — in-memory Storage и Presigner для тестов и локального dev.
// Не персистентен; контракт (size, ключи, ошибки, диапазон TTL) совпадает с
// боевым адаптером. Zero value пригоден: Ref.URL тогда — сам ключ.
type MemoryStorage struct {
	urls *URLBuilder

	mu   sync.RWMutex
	objs map[string]memObject
}

type memObject struct {
	data         []byte
	contentType  string
	etag         string
	lastModified time.Time
}

// NewMemoryStorage собирает хранилище; urls может быть nil.
func NewMemoryStorage(urls *URLBuilder) *MemoryStorage {
	return &MemoryStorage{urls: urls}
}

// Put сохраняет тело под сгенерированным ключом, соблюдая контракт size.
func (m *MemoryStorage) Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, fmt.Errorf("%w: %w", ErrUploadFailed, err)
	}
	contentType = NormalizeContentType(contentType)

	src := r
	if size >= 0 {
		src = io.LimitReader(r, size+1)
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return Ref{}, fmt.Errorf("%w: read body: %w", ErrUploadFailed, err)
	}
	if size >= 0 && int64(len(data)) != size {
		return Ref{}, fmt.Errorf("%w: %w: declared %d, actual %d", ErrUploadFailed, ErrSizeMismatch, size, len(data))
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, fmt.Errorf("%w: %w", ErrUploadFailed, err)
	}

	key := NewKey(contentType)
	sum := sha256.Sum256(data)
	m.mu.Lock()
	if m.objs == nil {
		m.objs = make(map[string]memObject)
	}
	m.objs[key] = memObject{
		data:         data,
		contentType:  contentType,
		etag:         hex.EncodeToString(sum[:16]),
		lastModified: time.Now().UTC(),
	}
	m.mu.Unlock()

	return Ref{
		Key:         key,
		URL:         m.urls.Build(key),
		ContentType: contentType,
		Size:        int64(len(data)),
	}, nil
}

// Get отдаёт копию сохранённых байтов: мутация в тесте не меняет хранилище.
func (m *MemoryStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}
	if key == "" {
		return nil, ObjectInfo{}, fmt.Errorf("%w: key required", ErrGetFailed)
	}

	m.mu.RLock()
	obj, ok := m.objs[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ObjectInfo{}, fmt.Errorf("%w: %w: %q", ErrGetFailed, ErrNotFound, key)
	}

	data := make([]byte, len(obj.data))
	copy(data, obj.data)
	return io.NopCloser(bytes.NewReader(data)), obj.info(), nil
}

// Stat возвращает метаданные объекта.
func (m *MemoryStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: %w", ErrStatFailed, err)
	}
	if key == "" {
		return ObjectInfo{}, fmt.Errorf("%w: key required", ErrStatFailed)
	}

	m.mu.RLock()
	obj, ok := m.objs[key]
	m.mu.RUnlock()
	if !ok {
		return ObjectInfo{}, fmt.Errorf("%w: %w: %q", ErrStatFailed, ErrNotFound, key)
	}
	return obj.info(), nil
}

// Delete идемпотентен; пустой ключ — ошибка, как и в боевом адаптере.
func (m *MemoryStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrDeleteFailed, err)
	}
	if key == "" {
		return fmt.Errorf("%w: key required", ErrDeleteFailed)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

// PresignGet — детерминированная псевдоподписанная ссылка: формат совпадает с
// публичным URL, параметры отражают ttl и response-headers.
func (m *MemoryStorage) PresignGet(ctx context.Context, key string, ttl time.Duration, opts ...PresignOption) (string, error) {
	params := NewPresignParams(opts...)
	q := url.Values{}
	if params.ContentDisposition != "" {
		q.Set("response-content-disposition", params.ContentDisposition)
	}
	if params.ContentType != "" {
		q.Set("response-content-type", params.ContentType)
	}
	return m.presign(ctx, "GET", key, ttl, q)
}

// PresignPut — псевдоподписанная ссылка на прямую загрузку.
func (m *MemoryStorage) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return m.presign(ctx, "PUT", key, ttl, url.Values{})
}

func (m *MemoryStorage) presign(ctx context.Context, method, key string, ttl time.Duration, q url.Values) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrPresignFailed, err)
	}
	if key == "" {
		return "", fmt.Errorf("%w: key required", ErrPresignFailed)
	}
	if err := ValidatePresignTTL(ttl); err != nil {
		return "", err
	}

	q.Set("x-presigned-method", method)
	q.Set("x-presigned-ttl", strconv.FormatInt(int64(ttl.Seconds()), 10))
	return m.urls.Build(key) + "?" + q.Encode(), nil
}

func (o memObject) info() ObjectInfo {
	return ObjectInfo{
		Size:         int64(len(o.data)),
		ContentType:  o.contentType,
		ETag:         o.etag,
		LastModified: o.lastModified,
	}
}
