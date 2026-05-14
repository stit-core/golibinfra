package natskit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"git.stit.tech/stit/golibinfra/logkit"
)

var ErrJetStreamDisabled = errors.New("natskit: jetstream is disabled")

type StreamBus interface {
	PublishStream(ctx context.Context, subject string, payload []byte) error
	SubscribeStream(ctx context.Context, stream, subject, durable string, handler func(ctx context.Context, payload []byte) error) error
}

type jsBus struct {
	js          jetstream.JetStream
	log         logkit.Logger
	sem         *semaphore.Weighted
	enabled     bool
	ackWait     time.Duration
	maxInFlight int
	instr       *natsInstruments
}

type disabledStreamBus struct{}

func (disabledStreamBus) PublishStream(context.Context, string, []byte) error {
	return ErrJetStreamDisabled
}

func (disabledStreamBus) SubscribeStream(context.Context, string, string, string, func(ctx context.Context, payload []byte) error) error {
	return ErrJetStreamDisabled
}

func NewStreamBus(conn *Conn, log logkit.Logger, cfg Config) (StreamBus, error) {
	if !cfg.JetStream.Enabled {
		return disabledStreamBus{}, nil
	}
	js, err := jetstream.New(conn.raw())
	if err != nil {
		return nil, fmt.Errorf("natskit: jetstream init: %w", err)
	}
	maxInFlight := cfg.JetStream.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = 100
	}
	ackWait := cfg.JetStream.AckWait
	if ackWait <= 0 {
		ackWait = 30 * time.Second
	}
	b := &jsBus{
		js:          js,
		log:         log,
		sem:         semaphore.NewWeighted(int64(maxInFlight)),
		enabled:     true,
		ackWait:     ackWait,
		maxInFlight: maxInFlight,
		instr:       newNatsInstruments(),
	}
	if err := b.ensureStreams(context.Background(), cfg); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *jsBus) ensureStreams(ctx context.Context, cfg Config) error {
	if len(cfg.JetStream.StreamNames) == 0 {
		return nil
	}
	retention, err := parseRetention(cfg.JetStream.Retention)
	if err != nil {
		return err
	}
	maxAge := cfg.JetStream.MaxAge
	if maxAge <= 0 {
		maxAge = 7 * 24 * time.Hour
	}
	for _, name := range cfg.JetStream.StreamNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		scfg := jetstream.StreamConfig{
			Name:      name,
			Subjects:  []string{name + ".>"},
			Retention: retention,
			MaxAge:    maxAge,
			Storage:   jetstream.FileStorage,
		}
		_, err := b.js.CreateStream(ctx, scfg)
		if err == nil {
			continue
		}
		if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			if _, uerr := b.js.UpdateStream(ctx, scfg); uerr != nil {
				return fmt.Errorf("natskit: update stream %s: %w", name, uerr)
			}
			continue
		}
		return fmt.Errorf("natskit: create stream %s: %w", name, err)
	}
	return nil
}

func parseRetention(s string) (jetstream.RetentionPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "limits":
		return jetstream.LimitsPolicy, nil
	case "interest":
		return jetstream.InterestPolicy, nil
	case "workqueue", "work_queue":
		return jetstream.WorkQueuePolicy, nil
	default:
		return 0, fmt.Errorf("natskit: unknown retention policy: %q", s)
	}
}

func (b *jsBus) PublishStream(ctx context.Context, subject string, payload []byte) error {
	if !b.enabled {
		return ErrJetStreamDisabled
	}

	msg, _, span := b.instr.startPublishSpan(ctx, subject, payload)
	start := time.Now()

	_, pubErr := b.js.PublishMsg(ctx, msg)
	dur := time.Since(start).Seconds()

	status := "ok"
	if pubErr != nil {
		status = "error"
		span.RecordError(pubErr)
		span.SetStatus(codes.Error, pubErr.Error())
	}
	span.End()

	b.instr.publishedTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("status", status),
		attribute.String("transport", "jetstream"),
	))
	b.instr.publishDuration.Record(ctx, dur,
		metric.WithAttributes(attribute.String("subject", subject), attribute.String("transport", "jetstream")),
	)

	if pubErr != nil {
		return fmt.Errorf("natskit: jetstream publish: %w", pubErr)
	}
	return nil
}

func (b *jsBus) SubscribeStream(ctx context.Context, stream, subject, durable string, handler func(ctx context.Context, payload []byte) error) error {
	if !b.enabled {
		return ErrJetStreamDisabled
	}
	cons, err := b.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: subject,
		AckWait:       b.ackWait,
		MaxAckPending: b.maxInFlight,
	})
	if err != nil {
		return fmt.Errorf("natskit: jetstream consumer: %w", err)
	}
	_, err = cons.Consume(func(msg jetstream.Msg) {
		if err := b.sem.Acquire(context.Background(), 1); err != nil {
			_ = msg.Nak()
			return
		}
		go func() {
			defer b.sem.Release(1)
			b.handleStreamOne(msg, handler)
		}()
	})
	if err != nil {
		return fmt.Errorf("natskit: jetstream consume: %w", err)
	}
	return nil
}

func (b *jsBus) handleStreamOne(msg jetstream.Msg, handler func(ctx context.Context, payload []byte) error) {
	subject := msg.Subject()
	subjAttr := metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("transport", "jetstream"),
	)
	b.instr.inflight.Add(context.Background(), 1, subjAttr)
	defer b.instr.inflight.Add(context.Background(), -1, subjAttr)

	// jetstream.Msg хранит header в nats.Header-форме — переиспользуем.
	natsMsg := &nats.Msg{Subject: subject, Data: msg.Data(), Header: msg.Headers()}
	ctx, span := b.instr.startConsumerSpan(context.Background(), natsMsg)
	start := time.Now()
	status := "ok"

	defer func() {
		dur := time.Since(start).Seconds()
		if rec := recover(); rec != nil {
			status = "panic"
			err := fmt.Errorf("natskit: jetstream handler panic: %v", rec)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			b.log.Error(ctx, "natskit: jetstream handler panic",
				"subject", subject,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
			_ = msg.Nak()
		}
		span.End()
		b.instr.consumedTotal.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("subject", subject),
			attribute.String("status", status),
			attribute.String("transport", "jetstream"),
		))
		b.instr.handleDuration.Record(context.Background(), dur, subjAttr)
	}()

	if err := handler(ctx, msg.Data()); err != nil {
		status = "handler_error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		b.log.Error(ctx, "natskit: jetstream handler error",
			"subject", subject, "err", err,
		)
		_ = msg.Nak()
		return
	}
	if err := msg.Ack(); err != nil {
		b.log.Error(ctx, "natskit: jetstream ack error",
			"subject", subject, "err", err,
		)
	}
}

func PublishStreamJSON[T any](b StreamBus, ctx context.Context, subject string, msg T) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natskit: marshal: %w", err)
	}
	return b.PublishStream(ctx, subject, data)
}

func SubscribeStreamJSON[T any](b StreamBus, ctx context.Context, stream, subject, durable string, h func(ctx context.Context, msg T) error) error {
	return b.SubscribeStream(ctx, stream, subject, durable, func(ctx context.Context, payload []byte) error {
		var v T
		if err := json.Unmarshal(payload, &v); err != nil {
			return fmt.Errorf("natskit: unmarshal: %w", err)
		}
		return h(ctx, v)
	})
}
