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
	sb        natskit.StreamBus
	namespace string
}

// NewStreamTransport адаптирует natskit JetStream (StreamBus) под Transport.
// Nats-Msg-Id = id строки — корректно, пока outbox-таблица одна (BIGSERIAL
// глобально уникален). Несколько таблиц на один стрим → NewStreamTransportFor.
func NewStreamTransport(sb natskit.StreamBus) Transport {
	return streamTransport{sb: sb}
}

// NewStreamTransportFor namespace'ит Nats-Msg-Id ("<namespace>:<id>") для
// нескольких outbox-инстансов, делящих один JetStream-стрим. Per-domain таблицы
// имеют независимый BIGSERIAL — голый id повторяется между таблицами, а
// stream-дедуп идёт по Msg-Id (не по subject) и молча уронил бы второй факт.
// Per-row дедуп ретраев сохраняется (та же строка → тот же Msg-Id). namespace
// обычно = имя Outbox-инстанса (outboxkit.New).
func NewStreamTransportFor(sb natskit.StreamBus, namespace string) Transport {
	return streamTransport{sb: sb, namespace: namespace}
}

func (t streamTransport) msgID(id int64) string {
	s := strconv.FormatInt(id, 10)
	if t.namespace == "" {
		return s
	}
	return t.namespace + ":" + s
}

func (t streamTransport) Publish(ctx context.Context, m OutMsg) error {
	return t.sb.PublishStream(ctx, m.Subject, m.Payload, natskit.WithMsgID(t.msgID(m.ID)))
}

func (t streamTransport) PublishBatch(ctx context.Context, msgs []OutMsg) ([]error, error) {
	sm := make([]natskit.StreamMsg, len(msgs))
	for i, m := range msgs {
		sm[i] = natskit.StreamMsg{
			Subject: m.Subject,
			Payload: m.Payload,
			MsgID:   t.msgID(m.ID),
		}
	}
	return t.sb.PublishStreamBatch(ctx, sm)
}
