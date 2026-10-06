package natskit_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
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

// labelValues — значения label'а key у серий метрики name; "" — серия без него.
func labelValues(d metrictest.Snapshot, name, key string) map[string]bool {
	out := map[string]bool{}
	for _, s := range d.Find(name) {
		out[s.Labels[key]] = true
	}
	return out
}

// instrRuns — прогоны TestBus_EmitsMetricsAndSpans в процессе (-count).
var instrRuns atomic.Int64

func TestBus_EmitsMetricsAndSpans(t *testing.T) {
	spans := installTracing(t)
	before := metrictest.Collect(t)

	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
	// Серия nats_inflight_messages прошлого прогона после +1 −1 не меняется и выпала бы из дельты.
	subject := subj(t, "instr"+strconv.FormatInt(instrRuns.Add(1), 10))

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
			switch s.Name() {
			case "natskit.publish " + subject:
				sawPublish = true
			case "natskit.handle " + subject:
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
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
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
			if s.Name() == "natskit.publish "+subject {
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

// Label subject у publish-метрик появляется только с WithMetricSubject:
// реальный subject публикации неограничен по кардинальности.
func TestBus_PublishMetricSubjectIsOptIn(t *testing.T) {
	before := metrictest.Collect(t)

	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

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
