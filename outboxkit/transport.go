package outboxkit

import (
	"context"
	"strconv"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Transport — порт публикации, через который Worker отдаёт события в брокер.
// Контракт: возврат без ошибки — только после подтверждённого персиста
// (JetStream PubAck); иначе Worker не метит событие published и переотправит
// следующим тиком. Publish — одиночный (ordered-режим), PublishBatch —
// async-батч; results[i] == nil = i-е подтверждено.
type Transport interface {
	Publish(ctx context.Context, msg OutMsg) error
	PublishBatch(ctx context.Context, msgs []OutMsg) (results []error, err error)
}

// OutMsg — событие outbox на публикацию. ID становится Nats-Msg-Id: повторная
// публикация той же строки (ретрай тика) дедуплицируется стримом.
type OutMsg struct {
	ID      int64
	Subject string
	Payload []byte
}

type streamTransport struct {
	sb natskit.StreamBus
}

// NewStreamTransport адаптирует natskit JetStream (StreamBus) под Transport.
func NewStreamTransport(sb natskit.StreamBus) Transport {
	return streamTransport{sb: sb}
}

func (t streamTransport) Publish(ctx context.Context, m OutMsg) error {
	return t.sb.PublishStream(ctx, m.Subject, m.Payload, natskit.WithMsgID(strconv.FormatInt(m.ID, 10)))
}

func (t streamTransport) PublishBatch(ctx context.Context, msgs []OutMsg) ([]error, error) {
	sm := make([]natskit.StreamMsg, len(msgs))
	for i, m := range msgs {
		sm[i] = natskit.StreamMsg{
			Subject: m.Subject,
			Payload: m.Payload,
			MsgID:   strconv.FormatInt(m.ID, 10),
		}
	}
	return t.sb.PublishStreamBatch(ctx, sm)
}
