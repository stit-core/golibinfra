// Package natstest — in-memory natskit.Bus и natskit.StreamBus для тестов
// потребителя: публикации записываются, подписчики получают их синхронно, без
// NATS и Docker; subject, имена, стримы и DLQ-петлю фейк проверяет как шина.
package natstest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/internal/natsname"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

// DefaultMaxPayload — предел тела публикации без WithMaxPayload: max_payload сервера NATS по
// умолчанию.
const DefaultMaxPayload = 1 << 20

// Recorder — фейк шины: реализует natskit.Bus, natskit.StreamBus и natskit.StreamChecker. Доставка
// синхронна — после Publish обработчики уже отработали, ждать нечего.
type Recorder struct {
	mu          sync.Mutex
	published   []Message
	subs        []*subscription
	handlerErrs []error
	publishErr  error
	nextSubID   int

	streams    map[string][]string // имя стрима → его subject'ы
	dlq        string
	maxDeliver int
	maxPayload int
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

// Option — настройка Recorder под конфиг шины сервиса.
type Option func(*Recorder)

// WithStream — стрим JetStream шины: subject'ы — как у стрима на сервере, без них — name.>, как у
// стрима из NATS_JS_STREAM_NAMES (NATS_JS_STREAM_MANAGED). Без стримов PublishStream, SubscribeStream
// и CheckStreams отвечают ошибкой, как JetStream без стримов.
func WithStream(name string, subjects ...string) Option {
	if len(subjects) == 0 {
		subjects = []string{name + ".>"}
	}
	return func(r *Recorder) {
		if r.streams == nil {
			r.streams = map[string][]string{}
		}
		r.streams[name] = subjects
	}
}

// WithDLQ — DLQ-subject шины (NATS_JS_DLQ_SUBJECT): stream-подписка с фильтром, который его
// накрывает, — natskit.ErrConfig, как у шины. Мёртвые сообщения фейк в DLQ не публикует.
func WithDLQ(subject string) Option {
	return func(r *Recorder) { r.dlq = subject }
}

// WithMaxDeliver — доставки stream-сообщения, чей обработчик вернул ошибку, как NATS_JS_MAX_DELIVER:
// ошибка, кроме обёрнутой natskit.ErrTerminal, — повторная доставка, всего не больше n. Без опции —
// одна доставка.
func WithMaxDeliver(n int) Option {
	return func(r *Recorder) { r.maxDeliver = n }
}

// WithMaxPayload — предел тела публикации (max_payload сервера); без опции — DefaultMaxPayload.
func WithMaxPayload(n int) Option {
	return func(r *Recorder) { r.maxPayload = n }
}

// New создаёт пустой Recorder; нулевой Recorder — то же, что New без опций.
func New(opts ...Option) *Recorder {
	r := &Recorder{}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

var (
	_ natskit.Bus           = (*Recorder)(nil)
	_ natskit.StreamBus     = (*Recorder)(nil)
	_ natskit.StreamChecker = (*Recorder)(nil)
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
	if err := r.checkPublish(subject, payload, jetStream); err != nil {
		return err
	}
	// JetStream ждёт PubAck под ctx публикации; Core-публикация его не читает — как у шины.
	if jetStream && ctx.Err() != nil {
		return fmt.Errorf("natstest: jetstream publish: %w", ctx.Err())
	}

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

// checkPublish — проверки шины до публикации: subject — по правилу natskit, тело — не больше
// max_payload, у stream-публикации — стрим, накрывающий subject.
func (r *Recorder) checkPublish(subject string, payload []byte, jetStream bool) error {
	switch {
	case !natsname.Subject(subject):
		return fmt.Errorf("natstest: publish: %w: %q", nats.ErrBadSubject, subject)
	case len(payload) > cmp.Or(r.maxPayload, DefaultMaxPayload):
		return fmt.Errorf("natstest: publish: %w: %d bytes, limit %d", nats.ErrMaxPayload, len(payload), cmp.Or(r.maxPayload, DefaultMaxPayload))
	case jetStream && r.streamFor(subject) == "":
		return fmt.Errorf("natstest: jetstream publish %q: %w", subject, jetstream.ErrNoStreamResponse)
	}
	return nil
}

// streamFor — объявленный стрим, чей subject накрывает subject (или фильтр) целиком; "" — такого нет.
func (r *Recorder) streamFor(subject string) string {
	for name, subjects := range r.streams {
		if covers(subjects, subject) {
			return name
		}
	}
	return ""
}

func covers(subjects []string, subject string) bool {
	return slices.ContainsFunc(subjects, func(s string) bool { return natskit.SubjectMatches(s, subject) })
}

// CheckStreams — как у шины (natskit.StreamChecker): каждый filter целиком накрыт объявленным
// стримом, иначе natskit.ErrNoStream.
func (r *Recorder) CheckStreams(_ context.Context, filters ...string) error {
	for _, f := range filters {
		if r.streamFor(f) == "" {
			return fmt.Errorf("%w: %s", natskit.ErrNoStream, f)
		}
	}
	return nil
}

// Deliver отдаёт сообщение подписчикам так, будто оно пришло из шины (публикация
// при этом не записывается), и возвращает ошибки обработчиков; невалидный subject — ошибка.
func (r *Recorder) Deliver(ctx context.Context, subject string, payload []byte, opts ...natskit.PublishOption) error {
	if !natsname.Subject(subject) {
		return fmt.Errorf("natstest: deliver: %w: %q", nats.ErrBadSubject, subject)
	}
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
		deliveries := 1
		if s.jetStream {
			deliveries = max(r.maxDeliver, 1)
		}
		// Повтор — как ретрай JetStream: ошибка, кроме терминальной, — до maxDeliver доставок.
		for range deliveries {
			err := handle(natskit.ContextWithMessage(ctx, subject, header), s.handler, payload)
			if err == nil {
				break
			}
			errs = append(errs, err)
			r.mu.Lock()
			r.handlerErrs = append(r.handlerErrs, err)
			r.mu.Unlock()
			if errors.Is(err, natskit.ErrTerminal) {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// handle зовёт обработчик и, как шина, ловит его panic: издателю она не уходит, а становится
// ошибкой обработчика (HandlerErrors) — stream-сообщение с ней переотправляется.
func handle(ctx context.Context, handler natskit.Handler, payload []byte) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("natstest: handler panic: %v", rec)
		}
	}()
	return handler(ctx, payload)
}

// Subscribe регистрирует fan-out подписку.
func (r *Recorder) Subscribe(ctx context.Context, subject string, handler natskit.Handler) (natskit.Subscription, error) {
	return r.subscribe(ctx, subject, "", "", handler)
}

// SubscribeQueue регистрирует подписку queue-группы: сообщение получает один её участник.
func (r *Recorder) SubscribeQueue(ctx context.Context, subject, queue string, handler natskit.Handler) (natskit.Subscription, error) {
	return r.subscribe(ctx, subject, queue, "", handler)
}

// SubscribeStream регистрирует stream-подписку; durable — её queue-группа. Как у шины: стрим —
// объявленный (WithStream), фильтр, накрывающий DLQ (WithDLQ), и второй фильтр на занятый durable
// — natskit.ErrConfig. Строже шины: фильтр вне subject'ов стрима и пустой durable она принимает,
// а подписка молчит.
func (r *Recorder) SubscribeStream(ctx context.Context, stream, subject, durable string, handler natskit.Handler) (natskit.Subscription, error) {
	subjects, declared := r.streams[stream]
	switch {
	case !natsname.Name(durable):
		return nil, fmt.Errorf("natstest: jetstream consumer: %w: %q", jetstream.ErrInvalidConsumerName, durable)
	case !declared:
		return nil, fmt.Errorf("natstest: jetstream consumer: stream %q: %w", stream, jetstream.ErrStreamNotFound)
	case natsname.Filter(subject) && !covers(subjects, subject):
		return nil, fmt.Errorf("natstest: jetstream consumer: filter %q is outside stream %s", subject, stream)
	case r.dlq != "" && natskit.SubjectMatches(subject, r.dlq):
		return nil, fmt.Errorf("%w: NATS_JS_DLQ_SUBJECT %q is matched by consumer filter %q: dead letters would loop back to the handler",
			natskit.ErrConfig, r.dlq, subject)
	}
	if held, taken := r.durableFilter(stream, durable); taken && held != subject {
		return nil, fmt.Errorf("%w: durable %q of stream %s is already subscribed with filter %q: filter %q would retarget the consumer and stop its deliveries — use one durable per filter",
			natskit.ErrConfig, durable, stream, held, subject)
	}
	return r.subscribe(ctx, subject, durable, stream, handler)
}

// durableFilter — фильтр живой stream-подписки на durable стрима: консьюмер у шины один, и второй
// фильтр перенацелил бы его.
func (r *Recorder) durableFilter(stream, durable string) (filter string, taken bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.subs {
		if s.jetStream && s.stream == stream && s.queue == durable {
			return s.filter, true
		}
	}
	return "", false
}

// subscribe: stream — стрим stream-подписки, пусто — Core.
func (r *Recorder) subscribe(ctx context.Context, subject, queue, stream string, handler natskit.Handler) (natskit.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !natsname.Filter(subject) {
		return nil, fmt.Errorf("natstest: subscribe: %w: %q", nats.ErrBadSubject, subject)
	}
	if !natsname.Queue(queue) {
		return nil, fmt.Errorf("natstest: subscribe: %w: %q", nats.ErrBadQueueName, queue)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSubID++
	s := &subscription{r: r, id: r.nextSubID, filter: subject, queue: queue, stream: stream, jetStream: stream != "", handler: handler}
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
	stream    string
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
