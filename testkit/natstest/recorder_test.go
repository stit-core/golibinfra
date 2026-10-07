package natstest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/testkit/natstest"
)

// orders — стрим ORDERS с subject'ами orders.>, как стрим, заведённый инфраструктурой.
var orders = natstest.WithStream("ORDERS", "orders.>")

func TestRecorder_PublishRecordsAndDelivers(t *testing.T) {
	r := natstest.New()
	ctx := context.Background()

	var got []byte
	var subject string
	var header string
	if _, err := r.Subscribe(ctx, "orders.*", func(hctx context.Context, payload []byte) error {
		got = payload
		subject = natskit.SubjectFromContext(hctx)
		header = natskit.HeadersFromContext(hctx).Get("Event-Type")
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	err := r.Publish(ctx, "orders.created", []byte(`{"id":1}`),
		natskit.WithHeader("Event-Type", "order.created"), natskit.WithMsgID("evt-1"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	if string(got) != `{"id":1}` {
		t.Fatalf("handler payload = %s", got)
	}
	if subject != "orders.created" {
		t.Fatalf("SubjectFromContext = %q", subject)
	}
	if header != "order.created" {
		t.Fatalf("Event-Type = %q", header)
	}

	published := r.Published()
	if len(published) != 1 || published[0].MsgID != "evt-1" || published[0].JetStream {
		t.Fatalf("published = %+v", published)
	}
	if got := r.PublishedOn("orders.>"); len(got) != 1 {
		t.Fatalf("PublishedOn = %+v", got)
	}
	if got := r.PublishedOn("other.>"); len(got) != 0 {
		t.Fatalf("PublishedOn(other) = %+v", got)
	}
}

func TestRecorder_QueueGroupGetsOneDelivery(t *testing.T) {
	r := natstest.New()
	ctx := context.Background()

	calls := 0
	h := func(context.Context, []byte) error { calls++; return nil }
	for range 3 {
		if _, err := r.SubscribeQueue(ctx, "jobs.run", "workers", h); err != nil {
			t.Fatalf("subscribe queue: %v", err)
		}
	}
	if err := r.Publish(ctx, "jobs.run", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// Core-публикация не доходит до stream-подписчика и наоборот: транспорты
// разведены, как в проде.
func TestRecorder_TransportsAreSeparate(t *testing.T) {
	r := natstest.New(orders)
	ctx := context.Background()

	core, stream := 0, 0
	if _, err := r.Subscribe(ctx, "orders.created", func(context.Context, []byte) error { core++; return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := r.SubscribeStream(ctx, "ORDERS", "orders.created", "worker", func(context.Context, []byte) error { stream++; return nil }); err != nil {
		t.Fatalf("subscribe stream: %v", err)
	}

	if err := r.Publish(ctx, "orders.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if core != 1 || stream != 0 {
		t.Fatalf("after Publish: core=%d stream=%d", core, stream)
	}

	if err := r.PublishStream(ctx, "orders.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish stream: %v", err)
	}
	if core != 1 || stream != 1 {
		t.Fatalf("after PublishStream: core=%d stream=%d", core, stream)
	}

	// Deliver имитирует входящее сообщение — его видят обе подписки.
	if err := r.Deliver(ctx, "orders.created", []byte(`{}`)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if core != 2 || stream != 2 {
		t.Fatalf("after Deliver: core=%d stream=%d", core, stream)
	}
}

func TestRecorder_DeliverReturnsHandlerError(t *testing.T) {
	r := natstest.New(orders)
	ctx := context.Background()
	boom := errors.New("boom")

	if _, err := r.SubscribeStream(ctx, "ORDERS", "orders.>", "worker", func(context.Context, []byte) error {
		return boom
	}); err != nil {
		t.Fatalf("subscribe stream: %v", err)
	}

	if err := r.Deliver(ctx, "orders.created", []byte(`{}`)); !errors.Is(err, boom) {
		t.Fatalf("Deliver = %v, want boom", err)
	}
	if errs := r.HandlerErrors(); len(errs) != 1 {
		t.Fatalf("HandlerErrors = %v", errs)
	}
}

func TestRecorder_UnsubscribeStopsDelivery(t *testing.T) {
	r := natstest.New()
	ctx := context.Background()

	calls := 0
	sub, err := r.Subscribe(ctx, "orders.created", func(context.Context, []byte) error { calls++; return nil })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if got := r.Subscriptions(); len(got) != 1 || got[0] != "orders.created" {
		t.Fatalf("Subscriptions = %v", got)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := r.Publish(ctx, "orders.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if calls != 0 {
		t.Fatalf("handler calls after Unsubscribe = %d", calls)
	}

	sub, err = r.Subscribe(ctx, "orders.created", func(context.Context, []byte) error { calls++; return nil })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sub.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := r.Publish(ctx, "orders.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if calls != 0 {
		t.Fatalf("handler calls after Drain = %d", calls)
	}
}

func TestRecorder_PublishErrorAndBatch(t *testing.T) {
	r := natstest.New(orders)
	ctx := context.Background()

	results, err := r.PublishStreamBatch(ctx, []natskit.StreamMsg{
		{Subject: "orders.created", Payload: []byte(`{"id":1}`), MsgID: "1"},
		{Subject: "orders.created", Payload: []byte(`{"id":2}`)},
	})
	if err != nil {
		t.Fatalf("PublishStreamBatch: %v", err)
	}
	if len(results) != 2 || results[0] != nil || results[1] != nil {
		t.Fatalf("results = %v", results)
	}
	if got := r.Published(); len(got) != 2 || got[0].MsgID != "1" || !got[0].JetStream {
		t.Fatalf("published = %+v", got)
	}

	boom := errors.New("bus is down")
	r.SetPublishError(boom)
	if err := r.Publish(ctx, "orders.created", []byte(`{}`)); !errors.Is(err, boom) {
		t.Fatalf("Publish = %v, want boom", err)
	}
	results, err = r.PublishStreamBatch(ctx, []natskit.StreamMsg{{Subject: "orders.created"}})
	if err != nil || !errors.Is(results[0], boom) {
		t.Fatalf("batch under publish error: results=%v err=%v", results, err)
	}

	r.SetPublishError(nil)
	r.Reset()
	if got := r.Published(); len(got) != 0 {
		t.Fatalf("Published after Reset = %+v", got)
	}
}

// Шаблон label'а `subject` доезжает до записи обоими путями: тест потребителя
// иначе не заметил бы, что публикация завела серию на каждый id.
func TestRecorder_RecordsMetricSubject(t *testing.T) {
	r := natstest.New(orders)
	ctx := context.Background()

	if err := r.PublishStream(ctx, "orders.created", []byte(`{}`),
		natskit.WithMetricSubject("orders.*")); err != nil {
		t.Fatalf("PublishStream: %v", err)
	}
	if _, err := r.PublishStreamBatch(ctx, []natskit.StreamMsg{
		{Subject: "orders.updated", Payload: []byte(`{}`), MetricSubject: "orders.*"},
		{Subject: "orders.deleted", Payload: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("PublishStreamBatch: %v", err)
	}

	got := r.Published()
	if len(got) != 3 {
		t.Fatalf("published = %+v", got)
	}
	if got[0].MetricSubject != "orders.*" || got[1].MetricSubject != "orders.*" {
		t.Fatalf("MetricSubject lost: %+v", got)
	}
	if got[2].MetricSubject != "" {
		t.Fatalf("MetricSubject invented for a message without it: %+v", got[2])
	}
}

func TestRecorder_SubscribeRejectsCancelledContext(t *testing.T) {
	r := natstest.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Subscribe(ctx, "orders.created", func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("Subscribe with a cancelled ctx = nil, want error")
	}
}

// Шина отвергает до сервера (и молча отбросил бы сервер) — фейк отвечает той же ошибкой: subject,
// фильтр, queue, durable, max_payload, стрим и DLQ-петля проверяются правилами natskit.
func TestRecorder_RejectsWhatTheBusRejects(t *testing.T) {
	r := natstest.New(orders, natstest.WithDLQ("orders.dlq"), natstest.WithMaxPayload(8))
	ctx := context.Background()
	h := func(context.Context, []byte) error { return nil }
	stream := func(stream, filter, durable string) error {
		_, err := r.SubscribeStream(ctx, stream, filter, durable, h)
		return err
	}
	for name, c := range map[string]struct {
		err  error
		want error
	}{
		"publish subject with a space":    {r.Publish(ctx, "orders created", nil), nats.ErrBadSubject},
		"publish empty subject":           {r.Publish(ctx, "", nil), nats.ErrBadSubject},
		"publish empty token":             {r.Publish(ctx, "orders..created", nil), nats.ErrBadSubject},
		"publish wildcard":                {r.Publish(ctx, "orders.*", nil), nats.ErrBadSubject},
		"publish over max_payload":        {r.Publish(ctx, "orders.created", []byte("123456789")), nats.ErrMaxPayload},
		"stream publish without stream":   {r.PublishStream(ctx, "billing.created", nil), jetstream.ErrNoStreamResponse},
		"subscribe empty token":           {func() error { _, err := r.Subscribe(ctx, "orders..x", h); return err }(), nats.ErrBadSubject},
		"subscribe > not last":            {func() error { _, err := r.Subscribe(ctx, "orders.>.x", h); return err }(), nats.ErrBadSubject},
		"queue with a space":              {func() error { _, err := r.SubscribeQueue(ctx, "orders.x", "a b", h); return err }(), nats.ErrBadQueueName},
		"durable with a dot":              {stream("ORDERS", "orders.created", "bad.durable"), jetstream.ErrInvalidConsumerName},
		"missing stream":                  {stream("MISSING", "orders.created", "d"), jetstream.ErrStreamNotFound},
		"filter outside the stream":       {stream("ORDERS", "billing.created", "d"), nil},
		"filter covers the DLQ subject":   {stream("ORDERS", "orders.>", "d"), natskit.ErrConfig},
		"deliver with an invalid subject": {r.Deliver(ctx, "orders created", nil), nats.ErrBadSubject},
		"stream check without stream":     {r.CheckStreams(ctx, "orders.*", "billing.>"), natskit.ErrNoStream},
	} {
		if c.err == nil || c.want != nil && !errors.Is(c.err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, c.err, c.want)
		}
	}
	if got := r.Published(); len(got) != 0 {
		t.Errorf("rejected publications recorded: %+v", got)
	}
	if err := r.CheckStreams(ctx, "orders.created", "orders.*"); err != nil {
		t.Errorf("CheckStreams of covered filters = %v, want nil", err)
	}
}

// Ошибка stream-обработчика — повторная доставка до WithMaxDeliver, как ретрай JetStream; ошибка с
// natskit.ErrTerminal — без повтора; Core-подписка — одна доставка.
func TestRecorder_RedeliversStreamHandlerErrors(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		opts      []natstest.Option
		err       error
		wantCalls int
	}{
		"default — one delivery": {nil, errors.New("boom"), 1},
		"up to MaxDeliver":       {[]natstest.Option{natstest.WithMaxDeliver(3)}, errors.New("boom"), 3},
		"terminal error":         {[]natstest.Option{natstest.WithMaxDeliver(3)}, fmt.Errorf("%w: bad payload", natskit.ErrTerminal), 1},
	} {
		r := natstest.New(append(c.opts, orders)...)
		calls, core := 0, 0
		if _, err := r.SubscribeStream(ctx, "ORDERS", "orders.created", "d", func(context.Context, []byte) error { calls++; return c.err }); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Subscribe(ctx, "orders.created", func(context.Context, []byte) error { core++; return c.err }); err != nil {
			t.Fatal(err)
		}
		if err := r.Deliver(ctx, "orders.created", []byte(`{}`)); !errors.Is(err, c.err) {
			t.Fatalf("%s: Deliver = %v, want the handler error", name, err)
		}
		if calls != c.wantCalls || core != 1 || len(r.HandlerErrors()) != c.wantCalls+1 {
			t.Errorf("%s: stream calls %d, core calls %d, handler errors %d; want %d, 1, %d", name, calls, core, len(r.HandlerErrors()), c.wantCalls, c.wantCalls+1)
		}
	}
	// Успех на повторной доставке её останавливает.
	r := natstest.New(orders, natstest.WithMaxDeliver(5))
	calls := 0
	if _, err := r.SubscribeStream(ctx, "ORDERS", "orders.created", "d", func(context.Context, []byte) error {
		calls++
		if calls < 2 {
			return errors.New("transient")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = r.PublishStream(ctx, "orders.created", []byte(`{}`))
	if calls != 2 {
		t.Errorf("calls = %d, want the redelivery to stop at the first success", calls)
	}
}
