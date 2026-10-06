// Package storagekit — S3-совместимое объектное хранилище: порт Storage
// (Put/Get/Stat/Delete), presigned-ссылки, публичный URL и in-memory адаптер.
// Кит доменно-нейтрален: стримит байты, mime/размер валидирует приложение.
package storagekit

import (
	"context"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"git.stit.tech/stit-core/golibinfra/idkit"
)

// DefaultContentType подставляется вместо пустого content-type: S3 всё равно
// сохранит объект с этим типом, и Ref не должен расходиться с метаданными.
const DefaultContentType = "application/octet-stream"

// Границы TTL presigned-ссылок в SigV4: одинаковы у боевого и in-memory адаптера.
const (
	// MinPresignTTL — минимальный срок жизни presigned-ссылки.
	MinPresignTTL = time.Second
	// MaxPresignTTL — максимальный срок жизни presigned-ссылки.
	MaxPresignTTL = 7 * 24 * time.Hour
)

// Ref — стабильная ссылка на сохранённый объект. URL публичный и не протухает.
type Ref struct {
	Key         string
	URL         string
	ContentType string
	Size        int64
}

// ObjectInfo — метаданные объекта в хранилище.
type ObjectInfo struct {
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// Storage — порт объектного хранилища.
type Storage interface {
	// Put стримит r под сгенерированным ключом. size >= 0 — точная длина тела:
	// расхождение даёт ErrSizeMismatch (в обёртке ErrUploadFailed), объект не
	// остаётся. size < 0 — длина неизвестна, тело читается до EOF; Ref.Size —
	// фактический размер. Пустой contentType нормализуется в DefaultContentType.
	Put(ctx context.Context, r io.Reader, contentType string, size int64) (Ref, error)
	// Get открывает объект на чтение; вызывающий обязан закрыть io.ReadCloser.
	// Отсутствующий ключ — ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Stat возвращает метаданные объекта; отсутствующий ключ — ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete идемпотентен: отсутствующий ключ — не ошибка. Пустой ключ —
	// ErrDeleteFailed.
	Delete(ctx context.Context, key string) error
}

// Presigner выдаёт краткоживущие presigned-ссылки на объекты сконфигуренного
// бакета: читатель (или загрузчик) обращается к хранилищу напрямую, без
// собственных кредов. ttl вне [MinPresignTTL, MaxPresignTTL] — ErrPresignFailed.
type Presigner interface {
	// PresignGet подписывает GET-ссылку; opts задают response-headers объекта.
	PresignGet(ctx context.Context, key string, ttl time.Duration, opts ...PresignOption) (string, error)
	// PresignPut подписывает PUT-ссылку для прямой загрузки клиентом.
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// PresignParams — response-headers, которые S3 вернёт по presigned GET-ссылке.
type PresignParams struct {
	ContentDisposition string
	ContentType        string
}

// PresignOption настраивает PresignParams.
type PresignOption func(*PresignParams)

// WithResponseContentDisposition задаёт Content-Disposition ответа (`attachment; filename="…"`).
func WithResponseContentDisposition(v string) PresignOption {
	return func(p *PresignParams) { p.ContentDisposition = v }
}

// WithResponseContentType задаёт Content-Type ответа поверх сохранённого.
func WithResponseContentType(v string) PresignOption {
	return func(p *PresignParams) { p.ContentType = v }
}

// NewPresignParams собирает параметры из опций — точка входа для адаптеров.
func NewPresignParams(opts ...PresignOption) PresignParams {
	var p PresignParams
	for _, opt := range opts {
		if opt != nil {
			opt(&p)
		}
	}
	return p
}

// ValidatePresignTTL проверяет срок жизни ссылки; ошибка обёрнута в ErrPresignFailed.
func ValidatePresignTTL(ttl time.Duration) error {
	if ttl < MinPresignTTL || ttl > MaxPresignTTL {
		return fmt.Errorf("%w: ttl %s outside [%s, %s]", ErrPresignFailed, ttl, MinPresignTTL, MaxPresignTTL)
	}
	return nil
}

// NormalizeContentType заменяет пустой content-type на DefaultContentType.
func NormalizeContentType(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return DefaultContentType
	}
	return contentType
}

// NewKey — `<uuidv7>.<ext>` (id — idkit.NewV7); ext выводится из contentType. uuidv7 даёт
// лексикографически возрастающие ключи (k-sortable по времени).
func NewKey(contentType string) string {
	return idkit.NewV7() + extFor(contentType)
}

// canonicalExt фиксирует расширение частых типов: mime-база ОС отличается от
// машины к машине, а ключ объекта должен воспроизводиться одинаково везде.
var canonicalExt = map[string]string{
	"application/json":         ".json",
	"application/octet-stream": "",
	"application/pdf":          ".pdf",
	"application/xml":          ".xml",
	"application/zip":          ".zip",
	"audio/mpeg":               ".mp3",
	"audio/ogg":                ".ogg",
	"audio/wav":                ".wav",
	"audio/webm":               ".weba",
	"image/avif":               ".avif",
	"image/gif":                ".gif",
	"image/jpeg":               ".jpg",
	"image/png":                ".png",
	"image/svg+xml":            ".svg",
	"image/webp":               ".webp",
	"text/csv":                 ".csv",
	"text/html":                ".html",
	"text/plain":               ".txt",
	"video/mp4":                ".mp4",
	"video/webm":               ".webm",
}

func extFor(contentType string) string {
	mediaType := contentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		mediaType = parsed
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	if ext, ok := canonicalExt[mediaType]; ok {
		return ext
	}
	exts, err := mime.ExtensionsByType(mediaType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	return exts[0]
}
