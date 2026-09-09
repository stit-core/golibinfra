package outboxkit

import (
	"context"
	"strconv"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Transport — порт публикации в брокер. Контракт: возврат без ошибки — только
// после подтверждённого персиста (JetStream PubAck), иначе Worker переотправит
// событие; results[i] == nil = i-е сообщение батча подтверждено.
type Transport interface {
	Publish(ctx context.Context, msg OutMsg) error
	PublishBatch(ctx context.Context, msgs []OutMsg) (results []error, err error)
}

// OutMsg — событие outbox на публикацию. ID становится Nats-Msg-Id: повторная
// публикация той же строки (ретрай тика) дедуплицируется стримом.
type OutMsg struct {
	// ID — идентификатор строки outbox'а.
	ID int64
	// Subject — subject публикации.
	Subject string
	// Payload — тело события.
	Payload []byte
}

type streamTransport struct {
	sb        natskit.StreamBus
	namespace string
	// metricSubject — конечное значение label `subject` publish-метрик natskit:
	// реальный subject события в label не годится (кардинальность домена).
	metricSubject string
}

// NewStreamTransport адаптирует natskit JetStream (StreamBus) под Transport.
// Nats-Msg-Id = id строки — корректно, пока outbox-таблица одна (BIGSERIAL
// глобально уникален). Несколько таблиц на один стрим → NewStreamTransportFor.
func NewStreamTransport(sb natskit.StreamBus) Transport {
	return streamTransport{sb: sb, metricSubject: DefaultInstance}
}

// NewStreamTransportFor namespace'ит Nats-Msg-Id ("<namespace>:<id>") для
// нескольких outbox-инстансов на одном JetStream-стриме: их BIGSERIAL'ы
// независимы, а дедуп идёт по Msg-Id и молча уронил бы второй факт.
func NewStreamTransportFor(sb natskit.StreamBus, namespace string) Transport {
	return streamTransport{sb: sb, namespace: namespace, metricSubject: namespace}
}

func (t streamTransport) msgID(id int64) string {
	s := strconv.FormatInt(id, 10)
	if t.namespace == "" {
		return s
	}
	return t.namespace + ":" + s
}

func (t streamTransport) Publish(ctx context.Context, m OutMsg) error {
	return t.sb.PublishStream(ctx, m.Subject, m.Payload,
		natskit.WithMsgID(t.msgID(m.ID)),
		natskit.WithMetricSubject(t.metricSubject),
	)
}

func (t streamTransport) PublishBatch(ctx context.Context, msgs []OutMsg) ([]error, error) {
	sm := make([]natskit.StreamMsg, len(msgs))
	for i, m := range msgs {
		sm[i] = natskit.StreamMsg{
			Subject:       m.Subject,
			Payload:       m.Payload,
			MsgID:         t.msgID(m.ID),
			MetricSubject: t.metricSubject,
		}
	}
	return t.sb.PublishStreamBatch(ctx, sm)
}
