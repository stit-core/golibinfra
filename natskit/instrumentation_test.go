package natskit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"git.stit.tech/stit/golibinfra/logkit"
	"git.stit.tech/stit/golibinfra/natskit"
	"git.stit.tech/stit/golibinfra/testkit"
)

func TestBus_EmitsMetricsAndSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}

	// Setup in-memory OTel SDK so we can assert what's emitted.
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	otel.SetTracerProvider(tp)

	t.Cleanup(func() {
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())
		otel.SetTracerProvider(sdktrace.NewTracerProvider())
	})

	fix := testkit.StartNATS(t)
	nc, err := natsgo.Connect(fix.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { nc.Close() })

	conn := natskit.ExportConn(nc)
	bus := natskit.NewBus(conn, logkit.Nop(), natskit.Config{})

	subject := "test.instr"
	var wg sync.WaitGroup
	wg.Add(1)
	if err := bus.Subscribe(context.Background(), subject, func(ctx context.Context, _ []byte) error {
		wg.Done()
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := bus.Publish(context.Background(), subject, "hello"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitCh := make(chan struct{})
	go func() { wg.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not invoked")
	}

	// metrics
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"nats_messages_published_total": false,
		"nats_publish_duration_seconds": false,
		"nats_messages_consumed_total":  false,
		"nats_handle_duration_seconds":  false,
		"nats_inflight_messages":        false,
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if _, ok := want[m.Name]; ok {
				want[m.Name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metric %s not emitted", name)
		}
	}

	// spans
	allSpans := spans.Ended()
	var sawPublish, sawHandle bool
	for _, s := range allSpans {
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
