// Package storagekit — S3-совместимое объектное хранилище: порт Storage
// (Put/Delete), стабильный публичный URL (URLBuilder) и in-memory адаптер
// для тестов. Kit доменно-нейтрален: image/file-семантика (валидация mime,
// размеров, декод картинки) — на стороне приложения в доменных VO.
package storagekit

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// Ref — стабильная ссылка на сохранённый объект. URL публичный и не
// протухает (read-side читает напрямую).
type Ref struct {
	Key         string
	URL         string
	ContentType string
	Size        int64
}

type Storage interface {
	// Put стримит r под сгенерированным ключом. size >= 0 — точная длина тела:
	// расхождение даёт ErrUploadFailed, объект не остаётся. size < 0 — длина
	// неизвестна, тело читается до EOF; Ref.Size — фактический размер.
	Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
	// Delete идемпотентен: отсутствующий ключ — не ошибка.
	Delete(ctx context.Context, key string) error
}

// Presigner выдаёт краткоживущую presigned-GET ссылку на объект (bucket+key),
// не требующую авторизации у читателя. TTL ограничивает окно доступа. bucket
// передаётся явно: потребитель может подписывать объекты вне сконфигуренного
// (upload-)бакета.
type Presigner interface {
	PresignedGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
}

// NewKey — `<uuidv7>.<ext>`; ext выводится из contentType. uuidv7 даёт
// лексикографически возрастающие ключи (k-sortable по времени).
func NewKey(contentType string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("%w: uuid: %w", ErrUploadFailed, err)
	}
	return id.String() + extFor(contentType), nil
}

// extFor покрывает форматы потребляемые доменными VO (ImageRef/FileRef).
// Неизвестный тип → без расширения (ключ остаётся валидным).
func extFor(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ""
	}
}
