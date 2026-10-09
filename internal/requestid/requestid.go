// Package requestid — ctx-ключ request-id, общий для серверов (httpkit, grpckit), клиентов
// (httpclient, grpcclient) и auditkit: им не нужно тянуть huma/chi ради одного ключа.
package requestid

import "context"

const (
	// Header — HTTP-заголовок и gRPC-metadata-ключ request-id.
	Header = "X-Request-ID"
	// MaxLen ограничивает id, принятый от клиента: он попадает в каждую строку лога.
	MaxLen = 128
)

type ctxKey struct{}

// From возвращает request-id из ctx или "" — идентификатор не обязателен.
func From(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// With кладёт request-id в ctx.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Valid — id от клиента принимается только печатным ASCII без пробелов.
func Valid(id string) bool {
	if id == "" || len(id) > MaxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}
