package natskit

import "context"

type subjectCtxKey struct{}

// SubjectFromContext возвращает NATS-субъект обрабатываемого сообщения внутри
// handler'а Subscribe/SubscribeStream (в т.ч. типизированных JSON-вариантов).
// Пусто вне обработчика. Нужно wildcard-подписчику (`a.*.cmd.>`), чтобы роутить
// по конкретному субъекту, не заставляя publisher'а дублировать его в payload.
func SubjectFromContext(ctx context.Context) string {
	s, _ := ctx.Value(subjectCtxKey{}).(string)
	return s
}

func withSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, subject)
}
