package natskit

import (
	"context"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// natsHeaderCarrier — TextMapCarrier на nats.Header. Через него
// W3C-trace-context (traceparent/tracestate) ездит вместе с сообщением,
// чтобы consumer запускал span как child producer'а.
type natsHeaderCarrier nats.Header

func (c natsHeaderCarrier) Get(key string) string {
	v := nats.Header(c).Values(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (c natsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

func (c natsHeaderCarrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

type natsInstruments struct {
	tracer   trace.Tracer
	prop     propagation.TextMapPropagator
	instance string

	publishedTotal  metric.Int64Counter
	publishDuration metric.Float64Histogram
	consumedTotal   metric.Int64Counter
	handleDuration  metric.Float64Histogram
	inflight        metric.Int64UpDownCounter
}

func newNatsInstruments(instance string) *natsInstruments {
	meter := otel.Meter("git.stit.tech/stit-core/golibinfra/natskit")
	tracer := otel.Tracer("git.stit.tech/stit-core/golibinfra/natskit")

	publishedTotal, _ := meter.Int64Counter(
		"nats_messages_published_total",
		metric.WithDescription("NATS messages published"),
	)
	publishDuration, _ := meter.Float64Histogram(
		"nats_publish_duration_seconds",
		metric.WithDescription("Time to publish a NATS message"),
		metric.WithUnit("s"),
	)
	consumedTotal, _ := meter.Int64Counter(
		"nats_messages_consumed_total",
		metric.WithDescription("NATS messages consumed"),
	)
	handleDuration, _ := meter.Float64Histogram(
		"nats_handle_duration_seconds",
		metric.WithDescription("Subscriber handler duration"),
		metric.WithUnit("s"),
	)
	inflight, _ := meter.Int64UpDownCounter(
		"nats_inflight_messages",
		metric.WithDescription("Messages currently being handled (per-subject)"),
	)

	if instance == "" {
		instance = DefaultInstance
	}

	return &natsInstruments{
		tracer:          tracer,
		prop:            otel.GetTextMapPropagator(),
		instance:        instance,
		publishedTotal:  publishedTotal,
		publishDuration: publishDuration,
		consumedTotal:   consumedTotal,
		handleDuration:  handleDuration,
		inflight:        inflight,
	}
}

// attrs добавляет к измерению имя инстанса — без него сигналы двух шин одного
// процесса складываются в одну серию.
func (in *natsInstruments) attrs(extra ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(append([]attribute.KeyValue{
		attribute.String("instance", in.instance),
	}, extra...)...)
}

// startPublishSpan создаёт producer-span и инжектит trace-context в
// nats.Header'ы, возвращая готовый msg + завершающую функцию.
func (in *natsInstruments) startPublishSpan(ctx context.Context, subject string, data []byte) (*nats.Msg, context.Context, trace.Span) {
	msg := &nats.Msg{Subject: subject, Data: data, Header: nats.Header{}}
	ctx, span := in.tracer.Start(
		ctx, "natskit.publish "+subject,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("nats"),
			semconv.MessagingDestinationName(subject),
			semconv.MessagingMessageBodySize(len(data)),
			attribute.String("instance", in.instance),
		),
	)
	in.prop.Inject(ctx, natsHeaderCarrier(msg.Header))
	return msg, ctx, span
}

// startConsumerSpan экстрактит trace-context из header'ов входящего msg
// и стартует consumer-span как child.
func (in *natsInstruments) startConsumerSpan(parent context.Context, msg *nats.Msg) (context.Context, trace.Span) {
	if msg.Header != nil {
		parent = in.prop.Extract(parent, natsHeaderCarrier(msg.Header))
	}
	parent = withSubject(parent, msg.Subject)
	parent = withHeaders(parent, msg.Header)
	return in.tracer.Start(
		parent, "natskit.handle "+msg.Subject,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("nats"),
			semconv.MessagingDestinationName(msg.Subject),
			semconv.MessagingMessageBodySize(len(msg.Data)),
			attribute.String("instance", in.instance),
		),
	)
}
