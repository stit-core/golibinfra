// Package natskit — pub/sub поверх NATS: интерфейс Bus, типизированные
// helper'ы PublishJSON[T]/SubscribeJSON[T], опц. JetStream, panic
// recovery в subscriber'ах и backpressure через семафор
// (NATS_MAX_IN_FLIGHT). W3C trace-context инжектится в nats.Header,
// чтобы consumer-спан был child'ом producer-спана. Эмитит
// nats_messages_{published,consumed}_total, nats_{publish,handle}_duration_seconds,
// nats_inflight_messages.
package natskit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"git.sftbmb.ru/sftbmb-core/golibinfra/logkit"
)

type Bus interface {
	Publish(ctx context.Context, subject string, payload any) error
	Subscribe(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error
}

type natsBus struct {
	conn  *nats.Conn
	log   logkit.Logger
	sem   *semaphore.Weighted
	instr *natsInstruments
}

func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus {
	maxInFlight := int64(cfg.Backpressure.MaxInFlight)
	if maxInFlight <= 0 {
		maxInFlight = 100
	}
	return &natsBus{
		conn:  conn.raw(),
		log:   log,
		sem:   semaphore.NewWeighted(maxInFlight),
		instr: newNatsInstruments(),
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

	attrs := metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("status", status),
	)
	b.instr.publishedTotal.Add(ctx, 1, attrs)
	b.instr.publishDuration.Record(ctx, dur,
		metric.WithAttributes(attribute.String("subject", subject)),
	)

	if pubErr != nil {
		return fmt.Errorf("natskit: publish: %w", pubErr)
	}
	return nil
}

func (b *natsBus) Subscribe(_ context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error {
	_, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		if err := b.sem.Acquire(context.Background(), 1); err != nil {
			b.log.Error(context.Background(), "natskit: semaphore acquire", "subject", subject, "err", err)
			return
		}
		go func() {
			defer b.sem.Release(1)
			b.handleOne(msg, subject, handler)
		}()
	})
	if err != nil {
		return fmt.Errorf("natskit: subscribe: %w", err)
	}
	return nil
}

func (b *natsBus) handleOne(msg *nats.Msg, subject string, handler func(ctx context.Context, payload []byte) error) {
	subjAttr := metric.WithAttributes(attribute.String("subject", subject))
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
			b.log.Error(ctx, "natskit: handler panic",
				"subject", subject,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
		}
		span.End()
		b.instr.consumedTotal.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("subject", subject),
			attribute.String("status", status),
		))
		b.instr.handleDuration.Record(context.Background(), dur, subjAttr)
	}()

	if err := handler(ctx, msg.Data); err != nil {
		status = "handler_error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		// Errors are logged at the handler-site if needed; bus log keeps a
		// trail so we can spot subjects with persistent failures.
		if !errors.Is(err, context.Canceled) {
			b.log.Error(ctx, "natskit: handler error", "subject", subject, "err", err)
		}
	}
}

func PublishJSON[T any](b Bus, ctx context.Context, subject string, msg T) error {
	return b.Publish(ctx, subject, msg)
}

func SubscribeJSON[T any](b Bus, ctx context.Context, subject string, h func(ctx context.Context, msg T) error) error {
	return b.Subscribe(ctx, subject, func(ctx context.Context, payload []byte) error {
		var v T
		if err := json.Unmarshal(payload, &v); err != nil {
			return fmt.Errorf("natskit: unmarshal: %w", err)
		}
		return h(ctx, v)
	})
}
