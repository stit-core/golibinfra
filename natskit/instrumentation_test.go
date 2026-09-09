package natskit_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func installOTel(t *testing.T) (*sdkmetric.ManualReader, *tracetest.SpanRecorder) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	// Без propagator'а Inject/Extract — no-op, и связность трассы не проверить.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
	return reader, spans
}

// collectUntil опрашивает reader, пока cond не вернёт true или не истечёт deadline:
// consume-метрики пишутся в defer обработчика — уже после сигнала тесту.
func collectUntil(t *testing.T, reader *sdkmetric.ManualReader, cond func(rm metricdata.ResourceMetrics) bool) metricdata.ResourceMetrics {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		if cond(rm) || time.Now().After(deadline) {
			return rm
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func metricNames(rm metricdata.ResourceMetrics) map[string]bool {
	out := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = true
		}
	}
	return out
}

// attrValues — значения одного атрибута по всем точкам counter'а.
func attrValues(rm metricdata.ResourceMetrics, metric, attr string) map[string]bool {
	out := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metric {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key(attr)); ok {
					out[v.AsString()] = true
				} else {
					out[""] = true
				}
			}
		}
	}
	return out
}

func TestBus_EmitsMetricsAndSpans(t *testing.T) {
	reader, spans := installOTel(t)

	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})
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
	rm := collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool {
		names := metricNames(rm)
		for _, n := range want {
			if !names[n] {
				return false
			}
		}
		return true
	})
	names := metricNames(rm)
	for _, n := range want {
		if !names[n] {
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
	_, spans := installOTel(t)

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
	reader, _ := installOTel(t)

	conn := connect(t, natsURL(t))
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	if err := bus.Publish(context.Background(), subj(t, "42"), []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	rm := collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool {
		return len(attrValues(rm, "nats_messages_published_total", "subject")) > 0
	})
	if got := attrValues(rm, "nats_messages_published_total", "subject"); !got[""] || len(got) != 1 {
		t.Fatalf("published_total subjects = %v, want a single series without the subject label", got)
	}

	if err := bus.Publish(context.Background(), subj(t, "43"), []byte("{}"), natskit.WithMetricSubject(nameFor(t)+".*")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	rm = collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool {
		return len(attrValues(rm, "nats_messages_published_total", "subject")) > 1
	})
	if got := attrValues(rm, "nats_messages_published_total", "subject"); !got[nameFor(t)+".*"] {
		t.Fatalf("published_total subjects = %v, want the pattern from WithMetricSubject", got)
	}
	if got := attrValues(rm, "nats_messages_published_total", "subject"); got[subj(t, "43")] {
		t.Fatalf("published_total carries the real subject: %v", got)
	}
}

// Label subject у JetStream-консьюмера — фильтр подписки, не реальный subject:
// иначе `orders.<id>.*` размножает серии на каждый id.
func TestStreamBus_ConsumeMetricsUseFilterSubject(t *testing.T) {
	reader, spans := installOTel(t)

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

	rm := collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool {
		return len(attrValues(rm, "nats_messages_consumed_total", "subject")) > 0
	})
	got := attrValues(rm, "nats_messages_consumed_total", "subject")
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
