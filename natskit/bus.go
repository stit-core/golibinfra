// Package natskit — pub/sub поверх NATS: интерфейс Bus (Subscribe/SubscribeQueue),
// типизированные helper'ы PublishJSON[T]/SubscribeJSON[T], опц. JetStream
// (StreamBus: батч-async публикация + дедуп по Nats-Msg-Id; poison-обработка
// consumer'а — MaxDeliver→DLQ/Term, NAK c backoff), panic recovery в
// subscriber'ах, backpressure через семафор (NATS_MAX_IN_FLIGHT) и graceful
// Close с ожиданием обработчиков. W3C trace-context инжектится в nats.Header,
// чтобы consumer-спан был child'ом producer-спана. Subject и Header сообщения
// доступны handler'у через SubjectFromContext(ctx)/HeadersFromContext(ctx).
// Эмитит nats_messages_{published,consumed}_total (consumed status:
// ok|handler_error|panic|dead), nats_{publish,handle}_duration_seconds,
// nats_inflight_messages.
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

type Bus interface {
	Publish(ctx context.Context, subject string, payload any) error
	// Subscribe — fan-out: сообщение получает каждый инстанс. ctx ограничивает
	// только регистрацию; время жизни подписки — Subscription и Close инстанса.
	Subscribe(ctx context.Context, subject string, handler Handler) (Subscription, error)
	// SubscribeQueue — load-balanced подписка: сообщение получает один инстанс
	// из queue-группы (горизонтальное масштабирование Core-консьюмеров).
	SubscribeQueue(ctx context.Context, subject, queue string, handler Handler) (Subscription, error)
}

type natsBus struct {
	c     *Conn
	conn  *nats.Conn
	log   logkit.Logger
	sem   *semaphore.Weighted
	instr *natsInstruments
}

func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus {
	return &natsBus{
		c:     conn,
		conn:  conn.raw(),
		log:   log,
		sem:   semaphore.NewWeighted(int64(intOr(cfg.Backpressure.MaxInFlight, defaultMaxInFlight))),
		instr: newNatsInstruments(cfg.Instance),
	}
}

func (b *natsBus) Publish(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("natskit: marshal payload: %w", err)
	}

	msg, _, span := b.instr.startPublishSpan(ctx, subject, data)
	start := time.Now()

	pubErr := b.conn.PublishMsg(msg)
	dur := time.Since(start).Seconds()

	status := "ok"
	if pubErr != nil {
		status = "error"
		span.RecordError(pubErr)
		span.SetStatus(codes.Error, pubErr.Error())
	}
	span.End()

	b.instr.publishedTotal.Add(ctx, 1, b.instr.attrs(
		attribute.String("subject", subject),
		attribute.String("status", status),
	))
	b.instr.publishDuration.Record(ctx, dur, b.instr.attrs(attribute.String("subject", subject)))

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
	subjAttr := b.instr.attrs(attribute.String("subject", subject))
	b.instr.inflight.Add(context.Background(), 1, subjAttr)
	defer b.instr.inflight.Add(context.Background(), -1, subjAttr)

	ctx, span := b.instr.startConsumerSpan(context.Background(), msg)
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
		span.End()
		b.instr.consumedTotal.Add(context.Background(), 1, b.instr.attrs(
			attribute.String("subject", subject),
			attribute.String("status", status),
		))
		b.instr.handleDuration.Record(context.Background(), dur, subjAttr)
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

func PublishJSON[T any](b Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}

func SubscribeJSON[T any](b Bus, ctx context.Context, subject string, h func(ctx context.Context, msg T) error) (Subscription, error) {
	return b.Subscribe(ctx, subject, decodeJSON(h))
}

func SubscribeQueueJSON[T any](b Bus, ctx context.Context, subject, queue string, h func(ctx context.Context, msg T) error) (Subscription, error) {
	return b.SubscribeQueue(ctx, subject, queue, decodeJSON(h))
}

func decodeJSON[T any](h func(ctx context.Context, msg T) error) Handler {
	return func(ctx context.Context, payload []byte) error {
		var v T
		if err := json.Unmarshal(payload, &v); err != nil {
			return fmt.Errorf("natskit: unmarshal: %w", err)
		}
		return h(ctx, v)
	}
}
