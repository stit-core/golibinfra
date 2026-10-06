package outboxkit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

func TestStreamTransport_MsgIDBareByDefault(t *testing.T) {
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransport(bus)
	if _, err := tr.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 42, Subject: "amocrm.x"}}); err != nil {
		t.Fatal(err)
	}
	msgs := bus.msgs()
	if got := msgs[0].MsgID; got != "42" {
		t.Fatalf("MsgID = %q, want %q", got, "42")
	}
	// Без шаблона subject'а — ни label, ни шаблона в сигналах natskit, а не имя инстанса (KO14).
	if got := msgs[0].MetricSubject; got != "" {
		t.Fatalf("MetricSubject = %q, want empty", got)
	}
}

func TestStreamTransportFor_NamespacesMsgID(t *testing.T) {
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransportFor(bus, "account")
	if _, err := tr.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 42, Subject: "amocrm.x"}}); err != nil {
		t.Fatal(err)
	}
	msgs := bus.msgs()
	if got := msgs[0].MsgID; got != "account:42" {
		t.Fatalf("MsgID = %q, want %q", got, "account:42")
	}
}

// Ordered-режим ходит через одиночный Publish — тот же Msg-Id и тот же label.
func TestStreamTransport_SinglePublishCarriesMsgID(t *testing.T) {
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransportFor(bus, "orders")
	if err := tr.Publish(context.Background(), outboxkit.OutMsg{ID: 7, Subject: "order.created", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	msgs := bus.msgs()
	if len(msgs) != 1 {
		t.Fatalf("published %d msgs, want 1", len(msgs))
	}
	if msgs[0].MsgID != "orders:7" || msgs[0].MetricSubject != "" || msgs[0].Subject != "order.created" {
		t.Fatalf("unexpected publish: %+v", msgs[0])
	}
}

// Шаблон subject'а и контекст трассы события доходят до natskit обоими путями — батчем и
// одиночной публикацией ordered.
func TestStreamTransport_CarriesTemplateAndTraceParent(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	m := outboxkit.OutMsg{ID: 1, Subject: "order.42.created", MetricSubject: "order.*.created", TraceParent: sc}
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransport(bus)
	if _, err := tr.PublishBatch(context.Background(), []outboxkit.OutMsg{m}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for i, got := range bus.msgs() {
		if got.MetricSubject != "order.*.created" || !got.TraceParent.Equal(sc) {
			t.Fatalf("message %d: %+v, want the template and the event trace context", i, got)
		}
	}
}

// Один id в двух namespace'ах обязан давать разные Msg-Id — иначе stream-дедуп
// (по Msg-Id, не по subject) уронил бы второй факт.
func TestStreamTransportFor_NoCrossTableCollision(t *testing.T) {
	bus := &capturingBus{}
	co := outboxkit.NewStreamTransportFor(bus, "companies")
	le := outboxkit.NewStreamTransportFor(bus, "leads")
	if _, err := co.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := le.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 1}}); err != nil {
		t.Fatal(err)
	}
	msgs := bus.msgs()
	if msgs[0].MsgID == msgs[1].MsgID {
		t.Fatalf("cross-table MsgID collision at id=1: both %q", msgs[0].MsgID)
	}
}

// errBus — StreamBus, где каждое сообщение отвечает item, а батч целиком — batch.
type errBus struct{ item, batch error }

func (b errBus) PublishStream(context.Context, string, []byte, ...natskit.PublishOption) error {
	return b.item
}

func (b errBus) PublishStreamBatch(_ context.Context, msgs []natskit.StreamMsg) ([]error, error) {
	if b.batch != nil {
		return nil, b.batch
	}
	errs := make([]error, len(msgs))
	for i := range errs {
		errs[i] = b.item
	}
	return errs, nil
}

func (errBus) SubscribeStream(context.Context, string, string, string, natskit.Handler) (natskit.Subscription, error) {
	return nil, nil
}

// Сбой брокера целиком транспорт помечает ErrUnavailable — попытку строки он не тратит;
// «нет стрима» и отказ по самому сообщению остаются неудачей строки.
func TestStreamTransport_BrokerDownIsUnavailable(t *testing.T) {
	cases := []struct {
		err  error
		down bool
	}{
		{nats.ErrConnectionClosed, true},
		{nats.ErrConnectionReconnecting, true},
		{fmt.Errorf("natskit: jetstream publish: %w", nats.ErrNoServers), true},
		{nats.ErrTimeout, true},
		{jetstream.ErrAsyncPublishTimeout, true},
		{natskit.ErrJetStreamDisabled, true},
		{context.DeadlineExceeded, true},
		{context.Canceled, true},
		{jetstream.ErrNoStreamResponse, false},
		{nats.ErrMaxPayload, false},
	}
	ctx := context.Background()
	msgs := []outboxkit.OutMsg{{ID: 1, Subject: "order.created"}}
	for _, tc := range cases {
		err := outboxkit.NewStreamTransport(errBus{item: tc.err}).Publish(ctx, msgs[0])
		if errors.Is(err, outboxkit.ErrUnavailable) != tc.down || !errors.Is(err, tc.err) {
			t.Errorf("Publish: %v, want unavailable=%v", err, tc.down)
		}
		results, err := outboxkit.NewStreamTransport(errBus{item: tc.err}).PublishBatch(ctx, msgs)
		if err != nil || len(results) != 1 || errors.Is(results[0], outboxkit.ErrUnavailable) != tc.down {
			t.Errorf("PublishBatch results = %v, err = %v; want unavailable=%v for %v", results, err, tc.down, tc.err)
		}
		_, err = outboxkit.NewStreamTransport(errBus{batch: tc.err}).PublishBatch(ctx, msgs)
		if errors.Is(err, outboxkit.ErrUnavailable) != tc.down || !errors.Is(err, tc.err) {
			t.Errorf("PublishBatch batch error = %v, want unavailable=%v", err, tc.down)
		}
	}
}

// checkedBus — errBus со StreamChecker: проверка JetStream отвечает down.
type checkedBus struct {
	errBus
	down error
}

func (b checkedBus) CheckStreams(context.Context, ...string) error { return b.down }

// «Нет ответа от стрима» дают и subject без стрима, и сервер без JetStream: транспорт спрашивает
// шину о JetStream, и только её отказ — сбой брокера.
func TestStreamTransport_NoStreamResponse(t *testing.T) {
	jsOff := fmt.Errorf("%w: server: %w", natskit.ErrJetStreamDisabled, jetstream.ErrJetStreamNotEnabled)
	noStream := errBus{item: jetstream.ErrNoStreamResponse}
	cases := map[string]struct {
		bus  natskit.StreamBus
		down bool
	}{
		"сервер без JetStream":           {checkedBus{noStream, jsOff}, true},
		"JetStream жив — нет стрима":     {checkedBus{noStream, nil}, false},
		"шина без StreamChecker":         {noStream, false},
		"отказ по сообщению не уточняют": {checkedBus{errBus{item: nats.ErrMaxPayload}, jsOff}, false},
	}
	ctx := context.Background()
	msgs := []outboxkit.OutMsg{{ID: 1, Subject: "order.created"}}
	for name, tc := range cases {
		tr := outboxkit.NewStreamTransport(tc.bus)
		if err := tr.Publish(ctx, msgs[0]); errors.Is(err, outboxkit.ErrUnavailable) != tc.down {
			t.Errorf("%s: Publish = %v, want unavailable=%v", name, err, tc.down)
		}
		if results, _ := tr.PublishBatch(ctx, msgs); len(results) != 1 || errors.Is(results[0], outboxkit.ErrUnavailable) != tc.down {
			t.Errorf("%s: PublishBatch results = %v, want unavailable=%v", name, results, tc.down)
		}
	}
}
