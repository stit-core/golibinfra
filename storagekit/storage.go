// Package storagekit — S3-совместимое объектное хранилище: порт Storage
// (Put/Delete), стабильный публичный URL (URLBuilder) и in-memory адаптер
// для тестов. Kit доменно-нейтрален: image/file-семантика (валидация mime,
// размеров, декод картинки) — на стороне приложения в доменных VO.
package storagekit

import (
	"context"
	"fmt"
	"io"

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
	// Put сохраняет не более size байт из r под сгенерированным ключом.
	// Тело больше declared size — ErrUploadFailed.
	Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
	Delete(ctx context.Context, key string) error
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
