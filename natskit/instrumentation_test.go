package natskit_test

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit/logtest"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// installTracing ставит провайдер трейсов и propagator на время теста; метрики
// снимает metrictest — его провайдер один на процесс.
func installTracing(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	// Без propagator'а Inject/Extract — no-op, и связность трассы не проверить.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
	return spans
}

// deltaUntil опрашивает метрики, пока cond не вернёт true или не истечёт deadline:
// consume-метрики пишутся в defer обработчика — уже после сигнала тесту.
func deltaUntil(t *testing.T, before metrictest.Snapshot, cond func(metrictest.Snapshot) bool) metrictest.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		d := metrictest.Collect(t).Sub(before)
		if cond(d) || time.Now().After(deadline) {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isPublishOf — producer-спан публикации в subject: без шаблона имя у всех одно, subject — в
// messaging.destination.name.
func isPublishOf(s sdktrace.ReadOnlySpan, subject string) bool {
	if s.SpanKind() != trace.SpanKindProducer {
		return false
	}
	for _, kv := range s.Attributes() {
		if kv.Key == "messaging.destination.name" {
			return kv.Value.AsString() == subject
		}
	}
	return false
}

// labelValues — значения label'а key у серий метрики name; "" — серия без него.
func labelValues(d metrictest.Snapshot, name, key string) map[string]bool {
	out := map[string]bool{}
	for _, s := range d.Find(name) {
		out[s.Labels[key]] = true
	}
	return out
}

func TestBus_EmitsMetricsAndSpans(t *testing.T) {
	spans := installTracing(t)
	before := metrictest.Collect(t)

	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	// Серия nats_inflight_messages прошлого прогона после +1 −1 не меняется и выпала бы из
	// дельты: subject свой на прогон (nameFor).
	subject := subj(t, "instr")

	handled := make(chan struct{}, 1)
	if _, err := bus.Subscribe(context.Background(), subject, func(context.Context, []byte) error {
		handled <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), subject, []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, handled, 3*time.Second, "handler")

	want := []string{
		"nats_messages_published_total",
		"nats_publish_duration_seconds",
		"nats_messages_consumed_total",
		"nats_handle_duration_seconds",
		"nats_inflight_messages",
	}
	d := deltaUntil(t, before, func(d metrictest.Snapshot) bool {
		for _, n := range want {
			if len(d.Find(n)) == 0 {
				return false
			}
		}
		return true
	})
	for _, n := range want {
		if len(d.Find(n)) == 0 {
			t.Errorf("metric %s not emitted", n)
		}
	}

	// Consumer-спан закрывается после возврата обработчика — позже сигнала тесту
	// и позже consume-метрик, поэтому spans.Ended() опрашивается, а не читается разом.
	eventually(t, 3*time.Second, "publish and handle spans recorded", func() bool {
		var sawPublish, sawHandle bool
		for _, s := range spans.Ended() {
			switch {
			case s.Name() == "natskit.publish" && isPublishOf(s, subject):
				sawPublish = true
			case s.Name() == "natskit.handle "+subject:
				sawHandle = true
			}
		}
		return sawPublish && sawHandle
	})
}

// Consumer-спан — child producer-спана: обещание связной трассы держится на
// инжекте trace-context в nats.Header.
func TestBus_ConsumerSpanIsChildOfProducer(t *testing.T) {
	spans := installTracing(t)

	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	subject := subj(t, "trace")

	handled := make(chan trace.SpanContext, 1)
	if _, err := bus.Subscribe(context.Background(), subject, func(ctx context.Context, _ []byte) error {
		handled <- trace.SpanContextFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), subject, []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	handlerSpan := recv(t, handled, 5*time.Second, "handler span context")

	var publishSpan trace.SpanContext
	eventually(t, 3*time.Second, "publish span recorded", func() bool {
		for _, s := range spans.Ended() {
			if isPublishOf(s, subject) {
				publishSpan = s.SpanContext()
				return true
			}
		}
		return false
	})

	if !handlerSpan.IsValid() {
		t.Fatal("handler ctx carries no span")
	}
	if handlerSpan.TraceID() != publishSpan.TraceID() {
		t.Fatalf("trace ids differ: handler %s, publish %s", handlerSpan.TraceID(), publishSpan.TraceID())
	}

	eventually(t, 3*time.Second, "handle span recorded", func() bool {
		for _, s := range spans.Ended() {
			if s.Name() == "natskit.handle "+subject {
				if s.Parent().SpanID() != publishSpan.SpanID() {
					t.Fatalf("handle span parent = %s, want publish span %s", s.Parent().SpanID(), publishSpan.SpanID())
				}
				return true
			}
		}
		return false
	})
}

// Имя producer-спана — шаблон из WithMetricSubject или `natskit.publish`: реальный subject с id
// размножил бы имена (KO12); шаблон — и в messaging.destination.template.
func TestBus_ProducerSpanNameIsTemplateNotSubject(t *testing.T) {
	spans := installTracing(t)
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	base := subj(t, "names")
	for i := range 3 {
		if err := bus.Publish(context.Background(), fmt.Sprintf("%s.%d", base, i), []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if err := bus.Publish(context.Background(), base+".9", []byte("x"), natskit.WithMetricSubject(base+".*")); err != nil {
		t.Fatalf("publish with template: %v", err)
	}
	names := map[string]int{}
	var template string
	for _, s := range spans.Ended() {
		if s.SpanKind() != trace.SpanKindProducer {
			continue
		}
		names[s.Name()]++
		for _, kv := range s.Attributes() {
			if kv.Key == "messaging.destination.template" {
				template = kv.Value.AsString()
			}
		}
	}
	if names["natskit.publish"] != 3 || names["natskit.publish "+base+".*"] != 1 || len(names) != 2 || template != base+".*" {
		t.Fatalf("producer span names = %v, template = %q; want 3 × natskit.publish and one named by the template", names, template)
	}
}

// WithTraceParent: producer-спан — ребёнок контекста события, span публикатора — ссылка на него;
// потребитель — в трассе события (KO13).
func TestBus_TraceParentContinuesEventTrace(t *testing.T) {
	spans := installTracing(t)
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	subject := subj(t, "parent")

	handled := make(chan trace.SpanContext, 1)
	if _, err := bus.Subscribe(context.Background(), subject, func(ctx context.Context, _ []byte) error {
		handled <- trace.SpanContextFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	tracer := otel.Tracer("test")
	_, event := tracer.Start(context.Background(), "request")
	event.End()
	tickCtx, tick := tracer.Start(context.Background(), "tick")
	err := bus.Publish(tickCtx, subject, []byte("x"), natskit.WithTraceParent(event.SpanContext()))
	tick.End()
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	handler := recv(t, handled, 5*time.Second, "handler span context")
	if handler.TraceID() != event.SpanContext().TraceID() {
		t.Fatalf("handler trace %s, want the event trace %s", handler.TraceID(), event.SpanContext().TraceID())
	}
	var producer sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if isPublishOf(s, subject) {
			producer = s
		}
	}
	if producer == nil {
		t.Fatal("no producer span")
	}
	if producer.Parent().SpanID() != event.SpanContext().SpanID() {
		t.Fatalf("producer parent = %s, want the event span %s", producer.Parent().SpanID(), event.SpanContext().SpanID())
	}
	links := producer.Links()
	if len(links) != 1 || links[0].SpanContext.SpanID() != tick.SpanContext().SpanID() {
		t.Fatalf("producer links = %+v, want one link to the publisher span", links)
	}
}

// Label subject у publish-метрик появляется только с WithMetricSubject:
// реальный subject публикации неограничен по кардинальности.
func TestBus_PublishMetricSubjectIsOptIn(t *testing.T) {
	before := metrictest.Collect(t)

	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	if err := bus.Publish(context.Background(), subj(t, "42"), []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	d := deltaUntil(t, before, func(d metrictest.Snapshot) bool {
		return len(labelValues(d, "nats_messages_published_total", "subject")) > 0
	})
	if got := labelValues(d, "nats_messages_published_total", "subject"); !got[""] || len(got) != 1 {
		t.Fatalf("published_total subjects = %v, want a single series without the subject label", got)
	}

	if err := bus.Publish(context.Background(), subj(t, "43"), []byte("{}"), natskit.WithMetricSubject(nameFor(t)+".*")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	d = deltaUntil(t, before, func(d metrictest.Snapshot) bool {
		return len(labelValues(d, "nats_messages_published_total", "subject")) > 1
	})
	if got := labelValues(d, "nats_messages_published_total", "subject"); !got[nameFor(t)+".*"] {
		t.Fatalf("published_total subjects = %v, want the pattern from WithMetricSubject", got)
	}
	if got := labelValues(d, "nats_messages_published_total", "subject"); got[subj(t, "43")] {
		t.Fatalf("published_total carries the real subject: %v", got)
	}
}

// Label subject у JetStream-консьюмера — фильтр подписки, не реальный subject:
// иначе `orders.<id>.*` размножает серии на каждый id.
func TestStreamBus_ConsumeMetricsUseFilterSubject(t *testing.T) {
	spans := installTracing(t)
	before := metrictest.Collect(t)

	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	filter := stream + ".>"

	handled := make(chan struct{}, 1)
	if _, err := sb.SubscribeStream(context.Background(), stream, filter, "wild", func(context.Context, []byte) error {
		handled <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subj(t, "42.created"), []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, handled, 10*time.Second, "handler")

	d := deltaUntil(t, before, func(d metrictest.Snapshot) bool {
		return len(labelValues(d, "nats_messages_consumed_total", "subject")) > 0
	})
	got := labelValues(d, "nats_messages_consumed_total", "subject")
	if !got[filter] {
		t.Fatalf("consumed_total subjects = %v, want filter %s", got, filter)
	}
	if got[subj(t, "42.created")] {
		t.Fatalf("consumed_total carries real subject: %v", got)
	}

	// Имя consumer-спана — тот же фильтр; реальный subject остаётся атрибутом.
	eventually(t, 3*time.Second, "handle span recorded", func() bool {
		for _, s := range spans.Ended() {
			if s.Name() != "natskit.handle "+filter {
				continue
			}
			var sawName, sawTemplate bool
			for _, a := range s.Attributes() {
				switch string(a.Key) {
				case "messaging.destination.name":
					sawName = a.Value.AsString() == subj(t, "42.created")
				case "messaging.destination.template":
					sawTemplate = a.Value.AsString() == filter
				}
			}
			if !sawName || !sawTemplate {
				t.Fatalf("handle span attributes = %v", s.Attributes())
			}
			return true
		}
		return false
	})
}

// Регрессия ROBS-03: текст ошибки и panic обработчика — потребителя — в consumer-спане под теми
// же масками, что в записи лога: e-mail и токен из ошибки не лежат в трассе открытыми.
func TestConsumerSpan_HandlerErrorTextIsMasked(t *testing.T) {
	const email, token = "ivan.petrov@example.com", "sk_live_51Mabcdef0123456789abcdef0123"
	failure := fmt.Errorf("charge customer %s: provider rejected Authorization: Bearer %s", email, token)
	spans := installTracing(t)
	stream := nameFor(t)
	conn := connect(t, natsURL(t))
	log := logtest.Capture()
	bus := newBus(t, conn, log, natskit.Config{})
	sb, err := natskit.NewStreamBus(jsConfig(stream), conn, log)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	ctx := context.Background()
	// JetStream переотправит сообщение после отказа: сигнал — без блокировки обработчика.
	done := make(chan struct{}, 16)
	signal := func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	fail := func(context.Context, []byte) error {
		defer signal()
		return failure
	}
	if _, err := bus.Subscribe(ctx, "core."+stream+".fail", fail); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := bus.Subscribe(ctx, "core."+stream+".panic", func(context.Context, []byte) error {
		defer signal()
		panic(failure)
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := sb.SubscribeStream(ctx, stream, stream+".>", "worker", fail); err != nil {
		t.Fatalf("subscribe stream: %v", err)
	}
	for _, subject := range []string{"core." + stream + ".fail", "core." + stream + ".panic"} {
		if err := bus.Publish(ctx, subject, []byte("x")); err != nil {
			t.Fatalf("publish %s: %v", subject, err)
		}
	}
	if err := sb.PublishStream(ctx, stream+".fail", []byte("x")); err != nil {
		t.Fatalf("publish stream: %v", err)
	}
	for range 3 {
		recv(t, done, 10*time.Second, "handler")
	}

	texts := func() (out []string) {
		for _, s := range spans.Ended() {
			if s.SpanKind() != trace.SpanKindConsumer || s.Status().Code != codes.Error {
				continue
			}
			out = append(out, s.Status().Description)
			for _, e := range s.Events() {
				for _, kv := range e.Attributes {
					if e.Name == "exception" && kv.Key == "exception.message" {
						out = append(out, kv.Value.AsString())
					}
				}
			}
		}
		return out
	}
	for deadline := time.Now().Add(5 * time.Second); len(texts()) < 6 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	got := texts()
	if len(got) < 6 {
		t.Fatalf("consumer spans with an error carry %d texts, want status and exception.message of three spans: %q", len(got), got)
	}
	for _, text := range got {
		if strings.Contains(text, email) || strings.Contains(text, token) || !strings.Contains(text, "provider rejected") {
			t.Errorf("span text must carry the error under the log masks: %q", text)
		}
	}
	if e, ok := log.Find(slog.LevelError, "natskit: handler error"); !ok || strings.Contains(e.Str("error"), email) || !slices.Contains(got, e.Str("error")) {
		t.Errorf("the span and the log record must carry the same masked text: log %q, spans %q", e.Str("error"), got)
	}
}
