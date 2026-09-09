package storagekit

import (
	"net/url"
	"strings"
)

// URLBuilder строит публичный URL объекта: `<base>/<bucket>/<key>` (path-style)
// или `<base>/<key>` (virtual-style — бакет уже в хосте base URL либо за CDN).
type URLBuilder struct {
	baseURL string
	bucket  string
	style   string
}

// NewURLBuilder собирает построитель; пустой style — URLStylePath.
func NewURLBuilder(baseURL, bucket, style string) *URLBuilder {
	if style == "" {
		style = URLStylePath
	}
	return &URLBuilder{
		baseURL: strings.TrimRight(baseURL, "/"),
		bucket:  bucket,
		style:   style,
	}
}

// Build возвращает публичный URL ключа. key — object key, а не URL-путь:
// каждый сегмент экранируется, разделители `/` сохраняются. Nil-приёмник даёт
// сам экранированный ключ — так zero value MemoryStorage остаётся пригодным.
func (b *URLBuilder) Build(key string) string {
	if b == nil {
		return escapeKey(key)
	}
	prefix := b.baseURL
	if b.style != URLStyleVirtual && b.bucket != "" {
		prefix += "/" + url.PathEscape(b.bucket)
	}
	return prefix + "/" + escapeKey(key)
}

func escapeKey(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
