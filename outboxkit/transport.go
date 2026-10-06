package outboxkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"

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
	// MetricSubject — шаблон subject'а для сигналов брокера (WithMetricSubject инстанса); пусто —
	// без шаблона: реальный subject с id в label и имя спана не годится.
	MetricSubject string
	// TraceParent — контекст трассы, в которой событие записано: доставка продолжает её.
	TraceParent trace.SpanContext
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
// нескольких outbox-инстансов на одном JetStream-стриме: их BIGSERIAL'ы
// независимы, а дедуп идёт по Msg-Id и молча уронил бы второй факт.
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
	err := t.sb.PublishStream(ctx, m.Subject, m.Payload,
		natskit.WithMsgID(t.msgID(m.ID)),
		natskit.WithMetricSubject(m.MetricSubject),
		natskit.WithTraceParent(m.TraceParent),
	)
	return unavailable(err, t.jetStreamDown(ctx, err))
}

func (t streamTransport) PublishBatch(ctx context.Context, msgs []OutMsg) ([]error, error) {
	sm := make([]natskit.StreamMsg, len(msgs))
	for i, m := range msgs {
		sm[i] = natskit.StreamMsg{
			Subject:       m.Subject,
			Payload:       m.Payload,
			MsgID:         t.msgID(m.ID),
			MetricSubject: m.MetricSubject,
			TraceParent:   m.TraceParent,
		}
	}
	results, err := t.sb.PublishStreamBatch(ctx, sm)
	down := t.jetStreamDown(ctx, results...)
	for i := range results {
		results[i] = unavailable(results[i], down)
	}
	return results, unavailable(err, down)
}

// jetStreamDown — ошибка проверки JetStream шины (StreamChecker), если публикация получила
// «нет ответа от стрима»: так отвечает и subject без стрима, и сервер без JetStream.
func (t streamTransport) jetStreamDown(ctx context.Context, errs ...error) error {
	checker, ok := t.sb.(natskit.StreamChecker)
	if !ok || !slices.ContainsFunc(errs, func(err error) bool { return errors.Is(err, jetstream.ErrNoStreamResponse) }) {
		return nil
	}
	return checker.CheckStreams(ctx)
}

// brokerDown — ошибки брокера целиком, а не строки: соединение, JetStream, таймаут PubAck,
// дедлайн тика. «Нет ответа от стрима» при живом JetStream сюда не входит: это строка или
// конфиг, а стримы subject'ов инстанса проверяет старт.
var brokerDown = []error{
	natskit.ErrJetStreamDisabled, context.Canceled, context.DeadlineExceeded,
	nats.ErrConnectionClosed, nats.ErrConnectionDraining, nats.ErrConnectionReconnecting,
	nats.ErrDisconnected, nats.ErrNoServers, nats.ErrTimeout,
	jetstream.ErrConnectionClosed, jetstream.ErrAsyncPublishTimeout, jetstream.ErrTooManyStalledMsgs,
	jetstream.ErrJetStreamNotEnabled, jetstream.ErrJetStreamNotEnabledForAccount,
	jetstream.ErrJetStreamPublisherClosed, jetstream.ErrServerShutdown,
}

// unavailable оборачивает сбой брокера в ErrUnavailable; jsDown — ошибка проверки JetStream
// при «нет ответа от стрима» (jetStreamDown).
func unavailable(err, jsDown error) error {
	if err == nil {
		return nil
	}
	if jsDown != nil && errors.Is(err, jetstream.ErrNoStreamResponse) {
		return fmt.Errorf("%w: %w: %w", ErrUnavailable, err, jsDown)
	}
	for _, down := range brokerDown {
		if errors.Is(err, down) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return err
}
