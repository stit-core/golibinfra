package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Span — обёртка для usecase/repo-методов: создаёт спан, маркирует ошибку, закрывает.
//
// Использование:
//
//	func (uc *Usecase) Register(ctx context.Context, in *Create) (u *User, err error) {
//	    ctx, end := telemetry.Span(ctx, "user.usecase.register")
//	    defer end(&err)
//	    // ... обычная логика
//	}
//
// Это шов на котором держится канон «не сыпать ручные spans по коду» — у тебя один паттерн.
func Span(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(*error)) {
	tracer := trace.SpanFromContext(ctx).TracerProvider().Tracer("app")
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attrs...))

	return ctx, func(errPtr *error) {
		if errPtr != nil && *errPtr != nil {
			span.RecordError(*errPtr)
			span.SetStatus(codes.Error, fmt.Sprintf("%v", *errPtr))
		}
		span.End()
	}
}
