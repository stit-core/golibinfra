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
// (event-type, schema-version, tenant и т.п.). nil вне обработчика. Содержит и
// W3C trace-context — это нормально, читай нужные ключи.
func HeadersFromContext(ctx context.Context) nats.Header {
	h, _ := ctx.Value(headerCtxKey{}).(nats.Header)
	return h
}

func withSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, subject)
}

func withHeaders(ctx context.Context, h nats.Header) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, headerCtxKey{}, h)
}
