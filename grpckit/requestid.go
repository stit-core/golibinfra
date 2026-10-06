package grpckit

import (
	"context"

	"git.stit.tech/stit-core/golibinfra/internal/requestid"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// HeaderRequestID — metadata-ключ сквозного идентификатора запроса (общий с
// HTTP-периметром: gRPC приводит имя ключа к нижнему регистру).
const HeaderRequestID = requestid.Header

// RequestIDFrom возвращает request-id вызова из ctx ("" — вне обработчика).
// Ключ общий с httpkit, поэтому gRPC-only сервис читает id, не импортируя
// HTTP-периметр.
func RequestIDFrom(ctx context.Context) string { return requestid.From(ctx) }

// gRPC-only сервис не импортирует httpkit, а поле request_id в каждой записи
// лога появляется только у зарегистрированного ctx-атрибута. RegisterAttr
// идемпотентен по ключу: сборка с httpkit и grpckit не даёт двух полей.
func init() {
	logkit.RegisterAttr("request_id", func(ctx context.Context) (any, bool) {
		id := requestid.From(ctx)
		return id, id != ""
	})
}
