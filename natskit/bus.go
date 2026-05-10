package natskit

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/nats-io/nats.go"
	"golang.org/x/sync/semaphore"

	"git.stit.tech/stit/golibinfra/logkit"
)

type Bus interface {
	Publish(ctx context.Context, subject string, payload any) error
	Subscribe(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error
}

type natsBus struct {
	conn *nats.Conn
	log  logkit.Logger
	sem  *semaphore.Weighted
}

func NewBus(conn *Conn, log logkit.Logger, cfg Config) Bus {
	maxInFlight := int64(cfg.Backpressure.MaxInFlight)
	if maxInFlight <= 0 {
		maxInFlight = 100
	}
	return &natsBus{
		conn: conn.raw(),
		log:  log,
		sem:  semaphore.NewWeighted(maxInFlight),
	}
}

func (b *natsBus) Publish(_ context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("natskit: marshal payload: %w", err)
	}
	if err := b.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("natskit: publish: %w", err)
	}
	return nil
}

func (b *natsBus) Subscribe(_ context.Context, subject string, handler func(ctx context.Context, payload []byte) error) error {
	_, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		_ = b.sem.Acquire(context.Background(), 1)
		go func() {
			defer b.sem.Release(1)
			defer func() {
				if rec := recover(); rec != nil {
					b.log.Error(context.Background(), "natskit: handler panic",
						"subject", subject,
						"panic", rec,
						"stack", string(debug.Stack()),
					)
				}
			}()
			if err := handler(context.Background(), msg.Data); err != nil {
				b.log.Error(context.Background(), "natskit: handler error", "subject", subject, "err", err)
			}
		}()
	})
	if err != nil {
		return fmt.Errorf("natskit: subscribe: %w", err)
	}
	return nil
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
