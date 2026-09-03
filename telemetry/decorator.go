package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "app"

// Span стартует спан от провайдера родителя из ctx, а без записывающего
// родителя — от глобального провайдера: так корневые спаны получают и job'ы,
// у которых в ctx спана нет. Finalizer с *error пишет RecordError + status=Error.
func Span(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(*error)) {
	tp := otel.GetTracerProvider()
	if parent := trace.SpanFromContext(ctx); parent.IsRecording() {
		tp = parent.TracerProvider()
	}
	ctx, span := tp.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))

	return ctx, func(errPtr *error) {
		if errPtr != nil && *errPtr != nil {
			span.RecordError(*errPtr)
			span.SetStatus(codes.Error, (*errPtr).Error())
		}
		span.End()
	}
}
