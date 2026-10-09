// Package natskit — pub/sub поверх NATS: Bus (Core, fire-and-forget) и StreamBus
// (JetStream, at-least-once с DLQ), backpressure, graceful Close и
// trace-propagation через nats.Header. Конфиг и сигналы — natskit/README.md.
package natskit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/semaphore"

	"git.stit.tech/stit-core/golibinfra/internal/natsname"
	"git.stit.tech/stit-core/golibinfra/internal/spanerr"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrTerminal — маркер «не ретраить»: обработчик, вернувший обёрнутую им ошибку,
// в JetStream получает Term (DLQ, если задан) вместо NAK. Для Core — обычная
// ошибка обработчика.
var ErrTerminal = errors.New("natskit: terminal")

// Bus — Core NATS: публикация без персиста, fan-out и queue-подписки.
type Bus interface {
	Publish(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	// Subscribe — fan-out: сообщение получает каждый инстанс. ctx ограничивает
	// только регистрацию; время жизни подписки — Subscription и Close инстанса.
	Subscribe(ctx context.Context, subject string, handler Handler) (Subscription, error)
	// SubscribeQueue — load-balanced подписка: сообщение получает один инстанс
	// из queue-группы (горизонтальное масштабирование Core-консьюмеров).
	SubscribeQueue(ctx context.Context, subject, queue string, handler Handler) (Subscription, error)
}

type natsBus struct {
	c              *Conn
	conn           *nats.Conn
	log            logkit.Logger
	sem            *semaphore.Weighted
	handlerTimeout time.Duration
	instr          *natsInstruments
}

// NewBus собирает Core-шину поверх соединения; обычный путь — Client.Bus(). Соединение уже
// открыто, поэтому проверяются только поля шины: NATS_MAX_IN_FLIGHT >= 1, NATS_HANDLER_TIMEOUT
// >= 0, иначе ErrConfig.
func NewBus(cfg Config, conn *Conn, log logkit.Logger) (Bus, error) {
	if err := cfg.validateBus(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logkit.Nop()
	}
	instance := cfg.Instance
	if instance == "" {
		instance = DefaultInstance
	}
	return &natsBus{
		c:              conn,
		conn:           conn.raw(),
		log:            log.With("instance", instance),
		sem:            semaphore.NewWeighted(int64(cfg.Backpressure.MaxInFlight)),
		handlerTimeout: cfg.HandlerTimeout,
		instr:          newNatsInstruments(instance, ""),
	}, nil
}

func (b *natsBus) Publish(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error {
	po := NewPublishSpec(opts...)
	// pubCtx несёт producer-span: измерение с ним получает exemplar на этот трейс.
	msg, pubCtx, span := b.instr.startPublishSpan(ctx, subject, payload, po)

	start := time.Now()
	pubErr := checkSubject(subject)
	if pubErr == nil {
		pubErr = b.conn.PublishMsg(msg)
	}
	b.instr.recordPublish(pubCtx, po, time.Since(start), pubErr, span)

	if pubErr != nil {
		return fmt.Errorf("natskit: publish: %w", pubErr)
	}
	return nil
}

// checkSubject: nats.go пропускает `a..b` и `a.*`, а сервер такую публикацию отбрасывает молча —
// правило natsname отвечает ошибкой сразу (им же проверяет фейк natstest).
func checkSubject(subject string) error {
	if !natsname.Subject(subject) {
		return fmt.Errorf("%w: %q", nats.ErrBadSubject, subject)
	}
	return nil
}

// checkFilter: `a.>.b` nats.go пропускает, сервер отвергает асинхронно — подписка молча пуста.
func checkFilter(filter string) error {
	if !natsname.Filter(filter) {
		return fmt.Errorf("%w: %q", nats.ErrBadSubject, filter)
	}
	return nil
}

func (b *natsBus) Subscribe(ctx context.Context, subject string, handler Handler) (Subscription, error) {
	return b.subscribe(ctx, subject, "", handler)
}

func (b *natsBus) SubscribeQueue(ctx context.Context, subject, queue string, handler Handler) (Subscription, error) {
	return b.subscribe(ctx, subject, queue, handler)
}

func (b *natsBus) subscribe(ctx context.Context, subject, queue string, handler Handler) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkFilter(subject); err != nil {
		return nil, fmt.Errorf("natskit: subscribe: %w", err)
	}
	return b.c.subscribe(func() (*subscription, error) {
		s := newSubscription(b.c, subject)
		cb := func(msg *nats.Msg) {
			// С Background-контекстом Acquire не ошибается: блокировка delivery-горутины и есть backpressure.
			_ = b.sem.Acquire(context.Background(), 1)
			untrack, ok := s.track()
			if !ok {
				b.sem.Release(1)
				return
			}
			go func() {
				defer untrack()
				defer b.sem.Release(1)
				b.handleOne(s.ctx, msg, subject, handler)
			}()
		}

		var (
			sub *nats.Subscription
			err error
		)
		if queue == "" {
			sub, err = b.conn.Subscribe(subject, cb)
		} else {
			sub, err = b.conn.QueueSubscribe(subject, queue, cb)
		}
		if err != nil {
			return nil, fmt.Errorf("natskit: subscribe: %w", err)
		}

		done := make(chan struct{})
		var closeOnce sync.Once
		sub.SetClosedHandler(func(string) { closeOnce.Do(func() { close(done) }) })
		s.done = done
		s.stop = func(drain bool) error {
			if drain {
				return sub.Drain()
			}
			return sub.Unsubscribe()
		}
		return s, nil
	})
}

func (b *natsBus) handleOne(parent context.Context, msg *nats.Msg, subject string, handler Handler) {
	// Метрики пишутся с ctx обработчика: он несёт consumer-span, без которого
	// у гистограммы нет exemplar'а в трейс этой обработки.
	ctx, span := b.instr.startConsumerSpan(parent, msg, subject)
	if b.handlerTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.handlerTimeout)
		defer cancel()
	}
	subjAttr := b.instr.subjectAttrs(subject)
	b.instr.inflight.Add(ctx, 1, subjAttr)
	defer b.instr.inflight.Add(ctx, -1, subjAttr)
	start := time.Now()

	status := "ok"
	defer func() {
		dur := time.Since(start).Seconds()
		if rec := recover(); rec != nil {
			status = "panic"
			spanerr.Record(span, fmt.Errorf("natskit: handler panic: %v", rec), b.log)
			b.log.Error(
				ctx, "natskit: handler panic",
				"subject", msg.Subject, "filter", subject,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
		b.instr.consumedTotal.Add(ctx, 1,
			b.instr.subjectAttrs(subject, attribute.String("status", status)))
		b.instr.handleDuration.Record(ctx, dur, subjAttr)
		span.End()
	}()

	if err := handler(ctx, msg.Data); err != nil {
		status = "handler_error"
		// Текст ошибки обработчика — потребителя: в спан он идёт под теми же масками, что в лог.
		spanerr.Record(span, err, b.log)
		if !errors.Is(err, context.Canceled) {
			// subject — сообщения, а не фильтр подписки: у wildcard по фильтру не понять, что упало.
			b.log.Error(ctx, "natskit: handler error", "subject", msg.Subject, "filter", subject, "error", err)
		}
	}
}

// PublishJSON маршалит msg в JSON и публикует его в Core NATS.
func PublishJSON[T any](b Bus, ctx context.Context, subject string, msg T, opts ...PublishOption) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natskit: marshal payload: %w", err)
	}
	return b.Publish(ctx, subject, data, opts...)
}

// SubscribeJSON — fan-out подписка с декодом payload в T.
func SubscribeJSON[T any](b Bus, ctx context.Context, subject string, h func(ctx context.Context, msg T) error) (Subscription, error) {
	return b.Subscribe(ctx, subject, decodeJSON(h))
}

// SubscribeQueueJSON — queue-подписка с декодом payload в T.
func SubscribeQueueJSON[T any](b Bus, ctx context.Context, subject, queue string, h func(ctx context.Context, msg T) error) (Subscription, error) {
	return b.SubscribeQueue(ctx, subject, queue, decodeJSON(h))
}

// decodeJSON: битый payload не станет валидным при ределивери — ошибка декода
// терминальна (JetStream отправит сообщение в DLQ, а не в цикл ретраев).
func decodeJSON[T any](h func(ctx context.Context, msg T) error) Handler {
	return func(ctx context.Context, payload []byte) error {
		var v T
		if err := json.Unmarshal(payload, &v); err != nil {
			return fmt.Errorf("%w: unmarshal payload: %w", ErrTerminal, err)
		}
		return h(ctx, v)
	}
}
