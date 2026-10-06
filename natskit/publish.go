package natskit

import (
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/trace"
)

// PublishOption настраивает одну публикацию — Bus.Publish и
// StreamBus.PublishStream принимают один и тот же набор опций.
type PublishOption func(*PublishSpec)

// PublishSpec — опции публикации в разобранном виде. Экспортирован ради фейков
// шины (testkit/natstest): реализация вне кита иначе не увидела бы ни заголовки,
// ни MsgID.
type PublishSpec struct {
	MsgID         string
	Header        nats.Header
	MetricSubject string
	TraceParent   trace.SpanContext
}

// NewPublishSpec собирает PublishSpec из опций.
func NewPublishSpec(opts ...PublishOption) PublishSpec {
	var s PublishSpec
	for _, o := range opts {
		o(&s)
	}
	return s
}

// WithMsgID ставит сообщению `Nats-Msg-Id`: в пределах окна StreamConfig.Duplicates
// стрим дедуплицирует повторную публикацию с тем же id (effectively-once персист
// при ретраях продьюсера). Для Core NATS — просто заголовок.
func WithMsgID(id string) PublishOption {
	return func(s *PublishSpec) { s.MsgID = id }
}

// WithHeader добавляет прикладной заголовок сообщения (`event-type`,
// `schema-version`, tenant): обработчик читает его через HeadersFromContext.
// Повторный вызов с тем же ключом добавляет второе значение.
func WithHeader(key, value string) PublishOption {
	return func(s *PublishSpec) {
		if s.Header == nil {
			s.Header = nats.Header{}
		}
		s.Header.Add(key, value)
	}
}

// WithMetricSubject задаёт шаблон subject'а (`alerts.employee.*`): значение label `subject`
// publish-метрик, имя producer-спана и `messaging.destination.template`. Без него label и шаблона
// нет, спан — `natskit.publish`: кит не заводит серию и имя на каждый id.
func WithMetricSubject(pattern string) PublishOption {
	return func(s *PublishSpec) { s.MetricSubject = pattern }
}

// WithTraceParent делает producer-span ребёнком контекста события (например, запроса, который
// записал событие в outbox), а span из ctx публикации — ссылкой (link) на него: трасса доставки
// продолжает трассу события, а не публикатора. Невалидный контекст ничего не меняет.
func WithTraceParent(sc trace.SpanContext) PublishOption {
	return func(s *PublishSpec) { s.TraceParent = sc }
}

// Headers — заголовки сообщения: копия заданных вызывающим плюс `Nats-Msg-Id`.
// Копия, потому что инжект trace-context не должен править nats.Header продьюсера.
func (s PublishSpec) Headers() nats.Header {
	h := nats.Header{}
	for k, v := range s.Header {
		h[k] = append([]string(nil), v...)
	}
	if s.MsgID != "" {
		h.Set(jetstream.MsgIDHeader, s.MsgID)
	}
	return h
}

func (s PublishSpec) jetStreamOpts() []jetstream.PublishOpt {
	if s.MsgID == "" {
		return nil
	}
	return []jetstream.PublishOpt{jetstream.WithMsgID(s.MsgID)}
}
