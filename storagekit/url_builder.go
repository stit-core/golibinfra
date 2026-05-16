package storagekit

import "strings"

// URLBuilder строит публичный стабильный URL `<base>/<bucket>/<key>`.
type URLBuilder struct {
	baseURL string
	bucket  string
}

func NewURLBuilder(baseURL, bucket string) *URLBuilder {
	return &URLBuilder{
		baseURL: strings.TrimRight(baseURL, "/"),
		bucket:  bucket,
	}
}

func (b *URLBuilder) Build(key string) string {
	return b.baseURL + "/" + b.bucket + "/" + key
}
