package anthropickit

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Ключи атрибутов gen_ai.* (OTel GenAI semconv). Промпты, ответы и аргументы
// tool-вызовов не пишутся никогда — только метаданные и счётчики токенов.
var (
	genAISystem        = attribute.Key("gen_ai.system")
	genAIOperationName = attribute.Key("gen_ai.operation.name")
	genAIRequestModel  = attribute.Key("gen_ai.request.model")
	genAIMaxTokens     = attribute.Key("gen_ai.request.max_tokens")
	genAIResponseModel = attribute.Key("gen_ai.response.model")
	genAIResponseID    = attribute.Key("gen_ai.response.id")
	genAIFinishReasons = attribute.Key("gen_ai.response.finish_reasons")
	genAIInputTokens   = attribute.Key("gen_ai.usage.input_tokens")
	genAIOutputTokens  = attribute.Key("gen_ai.usage.output_tokens")
)

func spanName(op string) string {
	if op == opCountTokens {
		return "anthropic.count_tokens"
	}
	return "anthropic.messages"
}

func genAIOperation(op string) string {
	if op == opCountTokens {
		return opCountTokens
	}
	return "chat"
}

// startSpan открывает собственный клиентский спан вызова; HTTP-спаны попыток
// SDK становятся дочерними.
func (c *Client) startSpan(ctx context.Context, op, model string, maxTokens int64) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		genAISystem.String("anthropic"),
		genAIOperationName.String(genAIOperation(op)),
		genAIRequestModel.String(model),
	}
	if maxTokens > 0 {
		attrs = append(attrs, genAIMaxTokens.Int64(maxTokens))
	}
	return c.tracer.Start(ctx, spanName(op),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
}

// annotateSpan дописывает метаданные ответа и usage. Refusal — успешный вызов,
// он виден только как finish reason.
func annotateSpan(span trace.Span, r *Response) {
	if !span.IsRecording() || r == nil {
		return
	}
	attrs := []attribute.KeyValue{
		genAIResponseModel.String(r.Model),
		genAIInputTokens.Int64(r.Usage.TotalInput()),
		genAIOutputTokens.Int64(r.Usage.OutputTokens),
	}
	if r.ID != "" {
		attrs = append(attrs, genAIResponseID.String(r.ID))
	}
	if r.StopReason != "" {
		attrs = append(attrs, genAIFinishReasons.StringSlice([]string{string(r.StopReason)}))
	}
	span.SetAttributes(attrs...)
}

func recordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
