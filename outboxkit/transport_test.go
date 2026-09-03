package outboxkit_test

import (
	"context"
	"testing"

	"git.stit.tech/stit-core/golibinfra/natskit"
	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

type capturingBus struct {
	batch []natskit.StreamMsg
}

func (b *capturingBus) PublishStream(context.Context, string, []byte, ...natskit.PublishOption) error {
	return nil
}

func (b *capturingBus) PublishStreamBatch(_ context.Context, msgs []natskit.StreamMsg) ([]error, error) {
	b.batch = append(b.batch, msgs...)
	return make([]error, len(msgs)), nil
}

func (b *capturingBus) SubscribeStream(context.Context, string, string, string, natskit.Handler) (natskit.Subscription, error) {
	return nil, nil
}

func TestStreamTransport_MsgIDBareByDefault(t *testing.T) {
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransport(bus)
	if _, err := tr.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 42, Subject: "amocrm.x"}}); err != nil {
		t.Fatal(err)
	}
	if got := bus.batch[0].MsgID; got != "42" {
		t.Fatalf("MsgID = %q, want %q", got, "42")
	}
}

func TestStreamTransportFor_NamespacesMsgID(t *testing.T) {
	bus := &capturingBus{}
	tr := outboxkit.NewStreamTransportFor(bus, "account")
	if _, err := tr.PublishBatch(context.Background(), []outboxkit.OutMsg{{ID: 42, Subject: "amocrm.x"}}); err != nil {
		t.Fatal(err)
	}
	if got := bus.batch[0].MsgID; got != "account:42" {
		t.Fatalf("MsgID = %q, want %q", got, "account:42")
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
	if bus.batch[0].MsgID == bus.batch[1].MsgID {
		t.Fatalf("cross-table MsgID collision at id=1: both %q", bus.batch[0].MsgID)
	}
}
