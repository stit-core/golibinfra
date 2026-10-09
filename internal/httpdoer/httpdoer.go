// Package httpdoer — минимальный HTTP-порт китов: internal/jwks и hubkit/client объявляют
// свой публичный Doer его алиасом, а не копией интерфейса (у oidckit — свой, того же вида).
package httpdoer

import "net/http"

// Doer — минимальный HTTP-клиент. Ему удовлетворяют *httpclient.Client
// (retry + breaker + OTel) и *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
