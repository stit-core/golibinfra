package natskit

import (
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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

// WithMetricSubject задаёт значение label `subject` publish-метрик: шаблон
// (`alerts.employee.*`), а не конкретный subject. Без него label отсутствует —
// кит не заводит серию на каждый id.
func WithMetricSubject(pattern string) PublishOption {
	return func(s *PublishSpec) { s.MetricSubject = pattern }
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
