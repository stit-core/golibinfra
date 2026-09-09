package natskit

import (
	"context"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

// transportJetStream — значение label `transport` у сигналов JetStream-пути;
// Core-путь этот label не ставит.
const transportJetStream = "jetstream"

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
	tracer    trace.Tracer
	prop      propagation.TextMapPropagator
	instance  string
	transport string

	publishedTotal  metric.Int64Counter
	publishDuration metric.Float64Histogram
	consumedTotal   metric.Int64Counter
	handleDuration  metric.Float64Histogram
	inflight        metric.Int64UpDownCounter
}

func newNatsInstruments(instance, transport string) *natsInstruments {
	meter := otelkit.Meter("natskit")
	if instance == "" {
		instance = DefaultInstance
	}
	return &natsInstruments{
		tracer:    otelkit.Tracer("natskit"),
		prop:      otel.GetTextMapPropagator(),
		instance:  instance,
		transport: transport,
		publishedTotal: otelkit.Must(meter.Int64Counter(
			"nats_messages_published_total",
			metric.WithDescription("NATS messages published"),
		)),
		publishDuration: otelkit.Must(meter.Float64Histogram(
			"nats_publish_duration_seconds",
			metric.WithDescription("Time to publish a NATS message"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
		consumedTotal: otelkit.Must(meter.Int64Counter(
			"nats_messages_consumed_total",
			metric.WithDescription("NATS messages consumed"),
		)),
		handleDuration: otelkit.Must(meter.Float64Histogram(
			"nats_handle_duration_seconds",
			metric.WithDescription("Subscriber handler duration"),
			metric.WithUnit("s"),
			otelkit.DurationBuckets(),
		)),
		inflight: otelkit.Must(meter.Int64UpDownCounter(
			"nats_inflight_messages",
			metric.WithDescription("Messages currently being handled (per subscription subject)"),
		)),
	}
}

// attrs добавляет к измерению имя инстанса и транспорт — без них сигналы двух
// шин одного процесса складываются в одну серию.
func (in *natsInstruments) attrs(extra ...attribute.KeyValue) metric.MeasurementOption {
	base := []attribute.KeyValue{attribute.String("instance", in.instance)}
	if in.transport != "" {
		base = append(base, attribute.String("transport", in.transport))
	}
	return metric.WithAttributes(append(base, extra...)...)
}

// subjectAttrs — label'ы подписки: subject фильтра, а не реальный subject
// сообщения (иначе wildcard-подписка размножает серии на каждый id).
func (in *natsInstruments) subjectAttrs(filter string, extra ...attribute.KeyValue) metric.MeasurementOption {
	return in.attrs(append([]attribute.KeyValue{attribute.String("subject", filter)}, extra...)...)
}

// startPublishSpan создаёт producer-span, собирает nats.Msg с заголовками
// вызывающего и инжектит в них trace-context.
func (in *natsInstruments) startPublishSpan(ctx context.Context, subject string, data []byte, po PublishSpec) (*nats.Msg, context.Context, trace.Span) {
	msg := &nats.Msg{Subject: subject, Data: data, Header: po.Headers()}
	attrs := []attribute.KeyValue{
		semconv.MessagingSystemKey.String("nats"),
		semconv.MessagingDestinationName(subject),
		semconv.MessagingMessageBodySize(len(data)),
		semconv.MessagingOperationTypeSend,
		attribute.String("instance", in.instance),
	}
	name := subject
	if po.MetricSubject != "" {
		name = po.MetricSubject
		attrs = append(attrs, semconv.MessagingDestinationTemplate(po.MetricSubject))
	}
	ctx, span := in.tracer.Start(
		ctx, "natskit.publish "+name,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attrs...),
	)
	in.prop.Inject(ctx, natsHeaderCarrier(msg.Header))
	return msg, ctx, span
}

// recordPublish закрывает producer-span и пишет publish-метрики. Label `subject`
// появляется только при WithMetricSubject: реальный subject публикации
// (`alerts.employee.<id>`) неограничен по кардинальности.
func (in *natsInstruments) recordPublish(ctx context.Context, po PublishSpec, dur time.Duration, err error, span trace.Span) {
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()

	extra := []attribute.KeyValue{attribute.String("status", status)}
	subject := make([]attribute.KeyValue, 0, 1)
	if po.MetricSubject != "" {
		subject = append(subject, attribute.String("subject", po.MetricSubject))
	}
	in.publishedTotal.Add(ctx, 1, in.attrs(append(subject, extra...)...))
	in.publishDuration.Record(ctx, dur.Seconds(), in.attrs(subject...))
}

// startConsumerSpan экстрактит trace-context из header'ов входящего msg и
// стартует consumer-span как child. Имя спана — фильтр подписки: реальный
// subject wildcard-подписки размножил бы имена.
func (in *natsInstruments) startConsumerSpan(parent context.Context, msg *nats.Msg, filter string) (context.Context, trace.Span) {
	if msg.Header != nil {
		parent = in.prop.Extract(parent, natsHeaderCarrier(msg.Header))
	}
	parent = ContextWithMessage(parent, msg.Subject, msg.Header)
	return in.tracer.Start(
		parent, "natskit.handle "+filter,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("nats"),
			semconv.MessagingDestinationName(msg.Subject),
			semconv.MessagingDestinationTemplate(filter),
			semconv.MessagingMessageBodySize(len(msg.Data)),
			semconv.MessagingOperationTypeProcess,
			attribute.String("instance", in.instance),
		),
	)
}
