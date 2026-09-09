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
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sync/semaphore"

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

// NewBus собирает Core-шину поверх соединения; обычный путь — Client.Bus().
func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus {
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
		sem:            semaphore.NewWeighted(int64(intOr(cfg.Backpressure.MaxInFlight, defaultMaxInFlight))),
		handlerTimeout: cfg.HandlerTimeout,
		instr:          newNatsInstruments(instance, ""),
	}
}

func (b *natsBus) Publish(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error {
	po := NewPublishSpec(opts...)
	// pubCtx несёт producer-span: измерение с ним получает exemplar на этот трейс.
	msg, pubCtx, span := b.instr.startPublishSpan(ctx, subject, payload, po)

	start := time.Now()
	pubErr := b.conn.PublishMsg(msg)
	b.instr.recordPublish(pubCtx, po, time.Since(start), pubErr, span)

	if pubErr != nil {
		return fmt.Errorf("natskit: publish: %w", pubErr)
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
	return b.c.subscribe(func() (*subscription, error) {
		s := &subscription{conn: b.c, subject: subject}
		cb := func(msg *nats.Msg) {
			// С Background-контекстом Acquire не ошибается: блокировка delivery-горутины и есть backpressure.
			_ = b.sem.Acquire(context.Background(), 1)
			untrack := s.track()
			go func() {
				defer untrack()
				defer b.sem.Release(1)
				b.handleOne(msg, subject, handler)
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

func (b *natsBus) handleOne(msg *nats.Msg, subject string, handler Handler) {
	// Метрики пишутся с ctx обработчика: он несёт consumer-span, без которого
	// у гистограммы нет exemplar'а в трейс этой обработки.
	ctx, span := b.instr.startConsumerSpan(b.c.handlerContext(), msg, subject)
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
			err := fmt.Errorf("natskit: handler panic: %v", rec)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			b.log.Error(
				ctx, "natskit: handler panic",
				"subject", subject,
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
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if !errors.Is(err, context.Canceled) {
			b.log.Error(ctx, "natskit: handler error", "subject", subject, "err", err)
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
