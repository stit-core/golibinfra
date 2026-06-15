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
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/semaphore"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

var ErrJetStreamDisabled = errors.New("natskit: jetstream is disabled")

// StreamMsg — единица батч-публикации в JetStream. MsgID (опц.) ставит
// Nats-Msg-Id: в пределах окна StreamConfig.Duplicates сообщения с тем же id
// дедуплицируются стримом (effectively-once персист при ретраях продьюсера).
type StreamMsg struct {
	Subject string
	Payload []byte
	MsgID   string
}

type StreamBus interface {
	PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	PublishStreamBatch(ctx context.Context, msgs []StreamMsg) (results []error, err error)
	SubscribeStream(ctx context.Context, stream, subject, durable string, handler func(ctx context.Context, payload []byte) error) error
}

type PublishOption func(*publishOptions)

type publishOptions struct {
	msgID string
}

// WithMsgID ставит Nats-Msg-Id для дедупа на стороне стрима (см. StreamMsg).
func WithMsgID(id string) PublishOption {
	return func(o *publishOptions) { o.msgID = id }
}

func pubOpts(po publishOptions) []jetstream.PublishOpt {
	if po.msgID == "" {
		return nil
	}
	return []jetstream.PublishOpt{jetstream.WithMsgID(po.msgID)}
}

type jsBus struct {
	js          jetstream.JetStream
	conn        *nats.Conn
	log         logkit.Logger
	sem         *semaphore.Weighted
	enabled     bool
	ackWait     time.Duration
	maxInFlight int
	maxDeliver  int
	nakBackoff  time.Duration
	dlqSubject  string
	instr       *natsInstruments
}

type disabledStreamBus struct{}

func (disabledStreamBus) PublishStream(context.Context, string, []byte, ...PublishOption) error {
	return ErrJetStreamDisabled
}

func (disabledStreamBus) PublishStreamBatch(context.Context, []StreamMsg) ([]error, error) {
	return nil, ErrJetStreamDisabled
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
		conn:        conn.raw(),
		log:         log,
		sem:         semaphore.NewWeighted(int64(maxInFlight)),
		enabled:     true,
		ackWait:     ackWait,
		maxInFlight: maxInFlight,
		maxDeliver:  cfg.JetStream.MaxDeliver,
		nakBackoff:  cfg.JetStream.NakBackoff,
		dlqSubject:  cfg.JetStream.DLQSubject,
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
	dup := cfg.JetStream.Duplicates
	if dup <= 0 {
		dup = 2 * time.Minute
	}
	if dup > maxAge {
		dup = maxAge // JetStream требует Duplicates ≤ MaxAge
	}
	for _, name := range cfg.JetStream.StreamNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		scfg := jetstream.StreamConfig{
			Name:       name,
			Subjects:   []string{name + ".>"},
			Retention:  retention,
			MaxAge:     maxAge,
			Storage:    jetstream.FileStorage,
			Duplicates: dup,
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

func (b *jsBus) PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error {
	if !b.enabled {
		return ErrJetStreamDisabled
	}
	var po publishOptions
	for _, o := range opts {
		o(&po)
	}

	msg, _, span := b.instr.startPublishSpan(ctx, subject, payload)
	start := time.Now()
	_, pubErr := b.js.PublishMsg(ctx, msg, pubOpts(po)...)
	b.recordPublish(ctx, subject, time.Since(start).Seconds(), pubErr, span)

	if pubErr != nil {
		return fmt.Errorf("natskit: jetstream publish: %w", pubErr)
	}
	return nil
}

// PublishStreamBatch публикует msgs async'ом и ждёт PubAck по каждому.
// results[i] == nil — сообщение подтверждено персистом; внешняя ошибка —
// сбой всего батча (jetstream disabled / отмена ctx). Порядок персиста —
// best-effort: при частичном сбое неподтверждённые уходят на ретрай и могут
// получить более поздний sequence; дедуп по MsgID гарантирует отсутствие дублей.
func (b *jsBus) PublishStreamBatch(ctx context.Context, msgs []StreamMsg) ([]error, error) {
	if !b.enabled {
		return nil, ErrJetStreamDisabled
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	errs := make([]error, len(msgs))
	futures := make([]jetstream.PubAckFuture, len(msgs))
	spans := make([]trace.Span, len(msgs))
	starts := make([]time.Time, len(msgs))

	for i, m := range msgs {
		msg, _, span := b.instr.startPublishSpan(ctx, m.Subject, m.Payload)
		spans[i] = span
		starts[i] = time.Now()

		f, err := b.js.PublishMsgAsync(msg, pubOpts(publishOptions{msgID: m.MsgID})...)
		if err != nil {
			errs[i] = err
			b.recordPublish(ctx, m.Subject, time.Since(starts[i]).Seconds(), err, span)
			spans[i] = nil
			continue
		}
		futures[i] = f
	}

	select {
	case <-b.js.PublishAsyncComplete():
	case <-ctx.Done():
		for i := range futures {
			if futures[i] != nil && errs[i] == nil {
				errs[i] = ctx.Err()
				b.recordPublish(ctx, msgs[i].Subject, time.Since(starts[i]).Seconds(), ctx.Err(), spans[i])
			}
		}
		return errs, fmt.Errorf("natskit: jetstream batch: %w", ctx.Err())
	}

	for i := range futures {
		if futures[i] == nil {
			continue
		}
		select {
		case <-futures[i].Ok():
			b.recordPublish(ctx, msgs[i].Subject, time.Since(starts[i]).Seconds(), nil, spans[i])
		case err := <-futures[i].Err():
			errs[i] = err
			b.recordPublish(ctx, msgs[i].Subject, time.Since(starts[i]).Seconds(), err, spans[i])
		}
	}
	return errs, nil
}

func (b *jsBus) recordPublish(ctx context.Context, subject string, dur float64, err error, span trace.Span) {
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
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
}

func (b *jsBus) SubscribeStream(ctx context.Context, stream, subject, durable string, handler func(ctx context.Context, payload []byte) error) error {
	if !b.enabled {
		return ErrJetStreamDisabled
	}
	ccfg := jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: subject,
		AckWait:       b.ackWait,
		MaxAckPending: b.maxInFlight,
	}
	if b.maxDeliver > 0 {
		ccfg.MaxDeliver = b.maxDeliver // server-side backstop; client-side Term ниже
	}
	cons, err := b.js.CreateOrUpdateConsumer(ctx, stream, ccfg)
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

	handlerErr, panicked := b.invoke(ctx, subject, handler, msg.Data())

	status := "ok"
	if handlerErr != nil {
		span.RecordError(handlerErr)
		span.SetStatus(codes.Error, handlerErr.Error())
		status = b.failStream(ctx, msg, subject, handlerErr, panicked)
	} else if err := msg.Ack(); err != nil {
		b.log.Error(ctx, "natskit: jetstream ack error", "subject", subject, "err", err)
	}

	span.End()
	b.instr.consumedTotal.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("subject", subject),
		attribute.String("status", status),
		attribute.String("transport", "jetstream"),
	))
	b.instr.handleDuration.Record(context.Background(), time.Since(start).Seconds(), subjAttr)
}

func (b *jsBus) invoke(ctx context.Context, subject string, handler func(ctx context.Context, payload []byte) error, data []byte) (err error, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("natskit: jetstream handler panic: %v", rec)
			panicked = true
			b.log.Error(ctx, "natskit: jetstream handler panic",
				"subject", subject, "panic", rec, "stack", string(debug.Stack()),
			)
		}
	}()
	return handler(ctx, data), false
}

// failStream решает судьбу неуспешного сообщения: при исчерпании MaxDeliver —
// DLQ (если задан) + Term (стоп ределивери); иначе NAK с опциональным backoff.
func (b *jsBus) failStream(ctx context.Context, msg jetstream.Msg, subject string, cause error, panicked bool) string {
	if b.terminal(msg) {
		b.deadLetter(ctx, msg, cause)
		if err := msg.Term(); err != nil {
			b.log.Error(ctx, "natskit: jetstream term error", "subject", subject, "err", err)
		}
		return "dead"
	}

	if b.nakBackoff > 0 {
		_ = msg.NakWithDelay(b.nakBackoff)
	} else {
		_ = msg.Nak()
	}
	if !panicked && !errors.Is(cause, context.Canceled) {
		b.log.Error(ctx, "natskit: jetstream handler error", "subject", subject, "err", cause)
	}
	if panicked {
		return "panic"
	}
	return "handler_error"
}

func (b *jsBus) terminal(msg jetstream.Msg) bool {
	if b.maxDeliver <= 0 {
		return false
	}
	md, err := msg.Metadata()
	if err != nil {
		return false
	}
	return md.NumDelivered >= uint64(b.maxDeliver)
}

func (b *jsBus) deadLetter(ctx context.Context, msg jetstream.Msg, cause error) {
	if b.dlqSubject == "" {
		return
	}
	h := nats.Header{}
	for k, v := range msg.Headers() {
		h[k] = v
	}
	h.Set("Natskit-Dlq-Origin", msg.Subject())
	h.Set("Natskit-Dlq-Error", cause.Error())
	if err := b.conn.PublishMsg(&nats.Msg{Subject: b.dlqSubject, Data: msg.Data(), Header: h}); err != nil {
		b.log.Error(ctx, "natskit: dlq publish", "subject", b.dlqSubject, "err", err)
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
