// Package httpdoer — один минимальный HTTP-порт на все киты: киты объявляют
// свой публичный Doer его алиасом, а не третьей копией интерфейса.
package httpdoer

import "net/http"

// Doer — минимальный HTTP-клиент. Ему удовлетворяют *httpclient.Client
// (retry + breaker + OTel) и *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
