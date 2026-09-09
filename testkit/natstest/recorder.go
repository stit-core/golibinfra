// Package natstest — in-memory natskit.Bus и natskit.StreamBus для тестов
// потребителя: публикации записываются, подписчики получают их синхронно, без
// NATS и Docker.
package natstest

import (
	"context"
	"errors"
	"sync"

	"github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

// Recorder — фейк шины: реализует natskit.Bus и natskit.StreamBus. Доставка
// синхронна — после Publish обработчики уже отработали, ждать нечего.
type Recorder struct {
	mu          sync.Mutex
	published   []Message
	subs        []*subscription
	handlerErrs []error
	publishErr  error
	nextSubID   int
}

// Message — записанная публикация.
type Message struct {
	Subject string
	Payload []byte
	Header  nats.Header
	MsgID   string
	// MetricSubject — шаблон label'а `subject` publish-метрик
	// (natskit.WithMetricSubject / StreamMsg.MetricSubject); пусто — label отсутствует.
	MetricSubject string
	JetStream     bool
}

// New создаёт пустой Recorder.
func New() *Recorder { return &Recorder{} }

var (
	_ natskit.Bus       = (*Recorder)(nil)
	_ natskit.StreamBus = (*Recorder)(nil)
)

// SetPublishError заставляет все публикации возвращать err (nil — снова успех):
// так тестируется ветка «шина недоступна».
func (r *Recorder) SetPublishError(err error) {
	r.mu.Lock()
	r.publishErr = err
	r.mu.Unlock()
}

// Reset очищает записи публикаций и ошибок обработчиков; подписки остаются.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.published, r.handlerErrs = nil, nil
	r.mu.Unlock()
}

// Published — все публикации в порядке отправки.
func (r *Recorder) Published() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.published...)
}

// PublishedOn — публикации в subject'ы, накрытые filter (wildcard'ы работают).
func (r *Recorder) PublishedOn(filter string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.published {
		if natskit.SubjectMatches(filter, m.Subject) {
			out = append(out, m)
		}
	}
	return out
}

// HandlerErrors — ошибки, которые вернули обработчики при доставке.
func (r *Recorder) HandlerErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.handlerErrs...)
}

// Publish записывает публикацию и синхронно доставляет её Core-подписчикам.
func (r *Recorder) Publish(ctx context.Context, subject string, payload []byte, opts ...natskit.PublishOption) error {
	return r.publish(ctx, subject, payload, false, opts)
}

// PublishStream записывает публикацию и доставляет её stream-подписчикам.
func (r *Recorder) PublishStream(ctx context.Context, subject string, payload []byte, opts ...natskit.PublishOption) error {
	return r.publish(ctx, subject, payload, true, opts)
}

// PublishStreamBatch публикует каждое сообщение батча; results[i] — исход i-го.
func (r *Recorder) PublishStreamBatch(ctx context.Context, msgs []natskit.StreamMsg) ([]error, error) {
	if len(msgs) == 0 {
		return nil, nil
	}
	results := make([]error, len(msgs))
	for i, m := range msgs {
		opts := []natskit.PublishOption{}
		if m.MsgID != "" {
			opts = append(opts, natskit.WithMsgID(m.MsgID))
		}
		if m.MetricSubject != "" {
			opts = append(opts, natskit.WithMetricSubject(m.MetricSubject))
		}
		for k, values := range m.Header {
			for _, v := range values {
				opts = append(opts, natskit.WithHeader(k, v))
			}
		}
		results[i] = r.publish(ctx, m.Subject, m.Payload, true, opts)
	}
	return results, nil
}

func (r *Recorder) publish(ctx context.Context, subject string, payload []byte, jetStream bool, opts []natskit.PublishOption) error {
	spec := natskit.NewPublishSpec(opts...)
	header := spec.Headers()

	r.mu.Lock()
	if err := r.publishErr; err != nil {
		r.mu.Unlock()
		return err
	}
	r.published = append(r.published, Message{
		Subject:       subject,
		Payload:       append([]byte(nil), payload...),
		Header:        header,
		MsgID:         spec.MsgID,
		MetricSubject: spec.MetricSubject,
		JetStream:     jetStream,
	})
	r.mu.Unlock()

	// Ошибку обработчика видно через HandlerErrors: публикация от неё не зависит.
	_ = r.deliver(ctx, subject, payload, header, func(s *subscription) bool { return s.jetStream == jetStream })
	return nil
}

// Deliver отдаёт сообщение подписчикам так, будто оно пришло из шины (публикация
// при этом не записывается), и возвращает ошибки обработчиков.
func (r *Recorder) Deliver(ctx context.Context, subject string, payload []byte, opts ...natskit.PublishOption) error {
	spec := natskit.NewPublishSpec(opts...)
	return r.deliver(ctx, subject, payload, spec.Headers(), func(*subscription) bool { return true })
}

func (r *Recorder) deliver(ctx context.Context, subject string, payload []byte, header nats.Header, accept func(*subscription) bool) error {
	r.mu.Lock()
	targets := make([]*subscription, 0, len(r.subs))
	queues := map[string]bool{}
	for _, s := range r.subs {
		if !accept(s) || !natskit.SubjectMatches(s.filter, subject) {
			continue
		}
		// Queue-группа: сообщение получает один подписчик группы, как в NATS.
		if s.queue != "" {
			if queues[s.queue] {
				continue
			}
			queues[s.queue] = true
		}
		targets = append(targets, s)
	}
	r.mu.Unlock()

	var errs []error
	for _, s := range targets {
		if err := s.handler(natskit.ContextWithMessage(ctx, subject, header), payload); err != nil {
			errs = append(errs, err)
			r.mu.Lock()
			r.handlerErrs = append(r.handlerErrs, err)
			r.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}

// Subscribe регистрирует fan-out подписку.
func (r *Recorder) Subscribe(ctx context.Context, subject string, handler natskit.Handler) (natskit.Subscription, error) {
	return r.subscribe(ctx, subject, "", false, handler)
}

// SubscribeQueue регистрирует подписку queue-группы: сообщение получает один её участник.
func (r *Recorder) SubscribeQueue(ctx context.Context, subject, queue string, handler natskit.Handler) (natskit.Subscription, error) {
	return r.subscribe(ctx, subject, queue, false, handler)
}

// SubscribeStream регистрирует stream-подписку; durable — её queue-группа.
func (r *Recorder) SubscribeStream(ctx context.Context, _, subject, durable string, handler natskit.Handler) (natskit.Subscription, error) {
	return r.subscribe(ctx, subject, durable, true, handler)
}

func (r *Recorder) subscribe(ctx context.Context, subject, queue string, jetStream bool, handler natskit.Handler) (natskit.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSubID++
	s := &subscription{r: r, id: r.nextSubID, filter: subject, queue: queue, jetStream: jetStream, handler: handler}
	r.subs = append(r.subs, s)
	return s, nil
}

// Subscriptions — фильтры зарегистрированных подписок в порядке регистрации.
func (r *Recorder) Subscriptions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.subs))
	for i, s := range r.subs {
		out[i] = s.filter
	}
	return out
}

type subscription struct {
	r         *Recorder
	id        int
	filter    string
	queue     string
	jetStream bool
	handler   natskit.Handler
}

// Unsubscribe снимает подписку; доставка синхронна, ждать нечего.
func (s *subscription) Unsubscribe() error {
	s.r.remove(s.id)
	return nil
}

// Drain снимает подписку: у синхронной доставки нет незавершённых обработчиков.
func (s *subscription) Drain(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.r.remove(s.id)
	return nil
}

func (r *Recorder) remove(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.subs {
		if s.id == id {
			r.subs = append(r.subs[:i], r.subs[i+1:]...)
			return
		}
	}
}
