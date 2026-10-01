package outboxkit

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Transport — порт публикации в брокер. Контракт: возврат без ошибки — только
// после подтверждённого персиста (JetStream PubAck), иначе Worker переотправит
// событие; results[i] == nil = i-е сообщение батча подтверждено. Сбой брокера целиком —
// не строки — обёрнут в ErrUnavailable: попытку строки он не тратит.
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
	return unavailable(t.sb.PublishStream(ctx, m.Subject, m.Payload,
		natskit.WithMsgID(t.msgID(m.ID)),
		natskit.WithMetricSubject(t.metricSubject),
	))
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
	results, err := t.sb.PublishStreamBatch(ctx, sm)
	for i := range results {
		results[i] = unavailable(results[i])
	}
	return results, unavailable(err)
}

// brokerDown — ошибки брокера целиком, а не строки: соединение, JetStream, таймаут PubAck,
// дедлайн тика. «Нет стрима для subject» сюда не входит: это строка или конфиг, а стримы
// subject'ов инстанса проверяет старт.
var brokerDown = []error{
	natskit.ErrJetStreamDisabled, context.Canceled, context.DeadlineExceeded,
	nats.ErrConnectionClosed, nats.ErrConnectionDraining, nats.ErrConnectionReconnecting,
	nats.ErrDisconnected, nats.ErrNoServers, nats.ErrTimeout,
	jetstream.ErrConnectionClosed, jetstream.ErrAsyncPublishTimeout, jetstream.ErrTooManyStalledMsgs,
	jetstream.ErrJetStreamNotEnabled, jetstream.ErrJetStreamNotEnabledForAccount,
	jetstream.ErrJetStreamPublisherClosed, jetstream.ErrServerShutdown,
}

func unavailable(err error) error {
	if err == nil {
		return nil
	}
	for _, down := range brokerDown {
		if errors.Is(err, down) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return err
}
