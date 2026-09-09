package natskit

import (
	"context"

	"github.com/nats-io/nats.go"
)

type (
	subjectCtxKey struct{}
	headerCtxKey  struct{}
)

// SubjectFromContext возвращает NATS-субъект обрабатываемого сообщения внутри
// handler'а Subscribe/SubscribeStream (в т.ч. типизированных JSON-вариантов).
// Пусто вне обработчика. Нужно wildcard-подписчику (`a.*.cmd.>`), чтобы роутить
// по конкретному субъекту, не заставляя publisher'а дублировать его в payload.
func SubjectFromContext(ctx context.Context) string {
	s, _ := ctx.Value(subjectCtxKey{}).(string)
	return s
}

// HeadersFromContext возвращает заголовки входящего сообщения внутри handler'а
// (event-type, schema-version, tenant и т.п. — см. WithHeader). nil вне
// обработчика. Содержит и W3C trace-context — это нормально, читай нужные ключи.
func HeadersFromContext(ctx context.Context) nats.Header {
	h, _ := ctx.Value(headerCtxKey{}).(nats.Header)
	return h
}

// ContextWithMessage кладёт в ctx subject и заголовки сообщения — ровно то, что
// внутри обработчика отдают SubjectFromContext и HeadersFromContext. Нужен
// фейкам шины (testkit/natstest): обработчик в тесте видит то же, что в проде.
func ContextWithMessage(ctx context.Context, subject string, h nats.Header) context.Context {
	ctx = context.WithValue(ctx, subjectCtxKey{}, subject)
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, headerCtxKey{}, h)
}
