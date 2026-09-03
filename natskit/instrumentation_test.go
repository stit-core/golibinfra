package natskit_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit"
)

func installOTel(t *testing.T) (*sdkmetric.ManualReader, *tracetest.SpanRecorder) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	t.Cleanup(func() {
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
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

func TestBus_EmitsMetricsAndSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	reader, spans := installOTel(t)

	fix := testkit.StartNATS(t)
	conn := connect(t, fix.URL)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	subject := "test.instr"
	handled := make(chan struct{}, 1)
	if _, err := bus.Subscribe(context.Background(), subject, func(context.Context, []byte) error {
		handled <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(context.Background(), subject, "hello"); err != nil {
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

	var sawPublish, sawHandle bool
	for _, s := range spans.Ended() {
		switch s.Name() {
		case "natskit.publish " + subject:
			sawPublish = true
		case "natskit.handle " + subject:
			sawHandle = true
		}
	}
	if !sawPublish {
		t.Error("publish span not recorded")
	}
	if !sawHandle {
		t.Error("handle span not recorded")
	}
}

// Label subject у JetStream-консьюмера — фильтр подписки, не реальный subject:
// иначе `orders.<id>.*` размножает серии на каждый id.
func TestStreamBus_ConsumeMetricsUseFilterSubject(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	reader, _ := installOTel(t)

	url := startJetStreamNATS(t)
	conn := connect(t, url)
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	handled := make(chan struct{}, 1)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.>", "wild", func(context.Context, []byte) error {
		handled <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), "ORDERS.42.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, handled, 10*time.Second, "handler")

	subjects := func(rm metricdata.ResourceMetrics) map[string]bool {
		out := map[string]bool{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name != "nats_messages_consumed_total" {
					continue
				}
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					continue
				}
				for _, dp := range sum.DataPoints {
					if v, ok := dp.Attributes.Value(attribute.Key("subject")); ok {
						out[v.AsString()] = true
					}
				}
			}
		}
		return out
	}
	rm := collectUntil(t, reader, func(rm metricdata.ResourceMetrics) bool { return len(subjects(rm)) > 0 })
	got := subjects(rm)
	if !got["ORDERS.>"] {
		t.Fatalf("consumed_total subjects = %v, want filter ORDERS.>", got)
	}
	if got["ORDERS.42.created"] {
		t.Fatalf("consumed_total carries real subject: %v", got)
	}
}
