package natskit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/semaphore"

	"git.stit.tech/stit-core/golibinfra/logkit"
)

// ErrJetStreamDisabled — JetStream выключен: NATS_JETSTREAM_ENABLED=false (любой метод
// StreamBus) или сервер без JetStream (CheckStreams).
var ErrJetStreamDisabled = errors.New("natskit: jetstream is disabled")

// ErrNoStream — subject'ы не накрыты ни одним стримом JetStream: публикация в них не получит PubAck.
var ErrNoStream = errors.New("natskit: no stream covers subjects")

// Заголовки DLQ-сообщения: контекст мёртвого сообщения для разбора.
const (
	HeaderDLQOrigin     = "Natskit-Dlq-Origin"
	HeaderDLQError      = "Natskit-Dlq-Error"
	HeaderDLQMsgID      = "Natskit-Dlq-Msg-Id"
	HeaderDLQStream     = "Natskit-Dlq-Stream"
	HeaderDLQConsumer   = "Natskit-Dlq-Consumer"
	HeaderDLQDeliveries = "Natskit-Dlq-Deliveries"
	HeaderDLQReason     = "Natskit-Dlq-Reason"
	HeaderDLQInstance   = "Natskit-Dlq-Instance"
)

// Значения HeaderDLQReason: почему сообщение больше не ретраится.
const (
	ReasonMaxDeliver = "max-deliver"
	ReasonTerminal   = "terminal"
)

const (
	dlqPublishTimeout = 5 * time.Second
	// minInProgressInterval — нижняя граница периода +WPI: тикер с нулевым
	// периодом паникует, а слишком частый — заваливает сервер.
	minInProgressInterval = 100 * time.Millisecond
	maxTermReasonLen      = 200
)

// StreamMsg — единица батч-публикации в JetStream.
type StreamMsg struct {
	Subject string
	Payload []byte
	// MsgID → Nats-Msg-Id: дедуп стримом в окне StreamConfig.Duplicates.
	MsgID string
	// Header — прикладные заголовки сообщения (аналог WithHeader).
	Header nats.Header
	// MetricSubject — значение label `subject` publish-метрик (аналог
	// WithMetricSubject); пусто — label отсутствует.
	MetricSubject string
}

func (m StreamMsg) spec() PublishSpec {
	return PublishSpec{MsgID: m.MsgID, Header: m.Header, MetricSubject: m.MetricSubject}
}

// StreamChecker — проверка стримов публикатора на старте; её реализует StreamBus natskit
// (natskit.New, Module), фейки шины — нет.
type StreamChecker interface {
	// CheckStreams — JetStream включён в конфиге и на сервере (иначе ErrJetStreamDisabled) и
	// каждый filter — subject или фильтр с `*`, `>` — целиком накрыт subject'ами одного стрима
	// (иначе ErrNoStream); сервер недоступен — его ошибка.
	CheckStreams(ctx context.Context, filters ...string) error
}

// StreamBus — JetStream: персист, дедуп по Nats-Msg-Id и durable-консьюмеры с
// poison-обработкой.
type StreamBus interface {
	PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error
	PublishStreamBatch(ctx context.Context, msgs []StreamMsg) (results []error, err error)
	// SubscribeStream — durable pull-консьюмер с explicit ack. ctx ограничивает
	// только регистрацию; время жизни подписки — Subscription и Close инстанса.
	SubscribeStream(ctx context.Context, stream, subject, durable string, handler Handler) (Subscription, error)
}

type jsBus struct {
	js             jetstream.JetStream
	c              *Conn
	conn           *nats.Conn
	log            logkit.Logger
	sem            *semaphore.Weighted
	instance       string
	enabled        bool
	ackWait        time.Duration
	handlerTimeout time.Duration
	maxInFlight    int
	maxDeliver     int
	nakBackoff     time.Duration
	nakMaxBackoff  time.Duration
	dlqSubject     string
	// dlqInStream — DLQ-subject накрыт стримом: копия уходит JetStream-публикацией
	// с PubAck. Иначе Core-публикацией: JetStream ждал бы PubAck, которого не будет.
	dlqInStream bool
	instr       *natsInstruments
}

type disabledStreamBus struct{}

func (disabledStreamBus) PublishStream(context.Context, string, []byte, ...PublishOption) error {
	return ErrJetStreamDisabled
}

func (disabledStreamBus) PublishStreamBatch(context.Context, []StreamMsg) ([]error, error) {
	return nil, ErrJetStreamDisabled
}

func (disabledStreamBus) SubscribeStream(context.Context, string, string, string, Handler) (Subscription, error) {
	return nil, ErrJetStreamDisabled
}

func (disabledStreamBus) CheckStreams(context.Context, ...string) error {
	return ErrJetStreamDisabled
}

// NewStreamBus собирает JetStream-шину и декларирует стримы конфига; при
// выключенном JetStream отдаёт заглушку, все методы которой возвращают
// ErrJetStreamDisabled.
func NewStreamBus(cfg Config, conn *Conn, log logkit.Logger) (StreamBus, error) {
	if !cfg.JetStream.Enabled {
		return disabledStreamBus{}, nil
	}
	// Соединение уже открыто, поэтому проверяются только JetStream-поля.
	if err := cfg.JetStream.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logkit.Nop()
	}
	instance := cfg.Instance
	if instance == "" {
		instance = DefaultInstance
	}
	js, err := jetstream.New(conn.raw(), jetstream.WithPublishAsyncTimeout(cfg.JetStream.PublishAsyncTimeout))
	if err != nil {
		return nil, fmt.Errorf("natskit: jetstream init: %w", err)
	}
	maxInFlight := cfg.JetStream.MaxInFlight
	ackWait := cfg.JetStream.AckWait
	handlerTimeout := cfg.JetStream.HandlerTimeout
	if handlerTimeout == 0 {
		handlerTimeout = ackWait
	}
	b := &jsBus{
		js:             js,
		c:              conn,
		conn:           conn.raw(),
		log:            log.With("instance", instance),
		sem:            semaphore.NewWeighted(int64(maxInFlight)),
		instance:       instance,
		enabled:        true,
		ackWait:        ackWait,
		handlerTimeout: handlerTimeout,
		maxInFlight:    maxInFlight,
		maxDeliver:     cfg.JetStream.MaxDeliver,
		nakBackoff:     cfg.JetStream.NakBackoff,
		nakMaxBackoff:  nakMaxBackoff(cfg.JetStream),
		dlqSubject:     cfg.JetStream.DLQSubject,
		instr:          newNatsInstruments(instance, transportJetStream),
	}
	if err := b.ensureStreams(context.Background(), cfg); err != nil {
		return nil, err
	}
	b.dlqInStream = b.dlqCoveredByStream(context.Background())
	return b, nil
}

func (b *jsBus) dlqCoveredByStream(ctx context.Context) bool {
	if b.dlqSubject == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, dlqPublishTimeout)
	defer cancel()

	name, err := b.js.StreamNameBySubject(ctx, b.dlqSubject)
	if err != nil {
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			b.log.Warn(ctx, "natskit: dlq stream lookup", "subject", b.dlqSubject, "err", err)
		}
		return false
	}
	b.log.Info(ctx, "natskit: dlq subject is stored in a stream", "subject", b.dlqSubject, "stream", name)
	return true
}

func (b *jsBus) ensureStreams(ctx context.Context, cfg Config) error {
	names := make([]string, 0, len(cfg.JetStream.StreamNames))
	for _, name := range cfg.JetStream.StreamNames {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if !cfg.JetStream.StreamManaged {
		for _, name := range names {
			if _, err := b.js.Stream(ctx, name); err != nil {
				return fmt.Errorf("natskit: stream %s is unmanaged and unavailable: %w", name, err)
			}
		}
		return nil
	}

	retention, err := parseRetention(cfg.JetStream.Retention)
	if err != nil {
		return err
	}
	maxAge := cfg.JetStream.MaxAge
	// JetStream требует Duplicates ≤ MaxAge.
	dup := min(cfg.JetStream.Duplicates, maxAge)
	for _, name := range names {
		want := jetstream.StreamConfig{
			Name:       name,
			Subjects:   []string{name + ".>"},
			Retention:  retention,
			MaxAge:     maxAge,
			Storage:    jetstream.FileStorage,
			Duplicates: dup,
		}
		if err := b.reconcileStream(ctx, want); err != nil {
			return err
		}
	}
	return nil
}

// reconcileStream создаёт отсутствующий стрим целиком, а в существующем
// доливает только subjects кита: Replicas, MaxBytes, лишние subjects и прочие
// настройки оператора остаются как есть.
func (b *jsBus) reconcileStream(ctx context.Context, want jetstream.StreamConfig) error {
	existing, err := b.js.Stream(ctx, want.Name)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		if _, err := b.js.CreateStream(ctx, want); err == nil {
			return nil
		} else if !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			return fmt.Errorf("natskit: create stream %s: %w", want.Name, err)
		}
		// Стрим появился между проверкой и созданием — сверяем subjects как у существующего.
		existing, err = b.js.Stream(ctx, want.Name)
	}
	if err != nil {
		return fmt.Errorf("natskit: stream %s: %w", want.Name, err)
	}

	cur := existing.CachedInfo().Config
	b.warnStreamDrift(ctx, want, cur)
	merged := unionSubjects(cur.Subjects, want.Subjects)
	if len(merged) == len(cur.Subjects) {
		return nil
	}
	cur.Subjects = merged
	if _, err := b.js.UpdateStream(ctx, cur); err != nil {
		return fmt.Errorf("natskit: update stream %s subjects: %w", want.Name, err)
	}
	return nil
}

// warnStreamDrift: существующий стрим кит не переписывает, поэтому смена
// NATS_JS_MAX_AGE и соседних переменных без предупреждения выглядела бы как
// применённая.
func (b *jsBus) warnStreamDrift(ctx context.Context, want, have jetstream.StreamConfig) {
	for _, f := range []struct {
		field      string
		want, have any
	}{
		{"retention", want.Retention.String(), have.Retention.String()},
		{"max_age", want.MaxAge.String(), have.MaxAge.String()},
		{"duplicates", want.Duplicates.String(), have.Duplicates.String()},
		{"storage", want.Storage.String(), have.Storage.String()},
	} {
		if f.want != f.have {
			b.log.Warn(ctx, "natskit: stream config drift",
				"stream", want.Name, "field", f.field, "want", f.want, "have", f.have)
		}
	}
}

func unionSubjects(have, want []string) []string {
	out := slices.Clone(have)
	for _, s := range want {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
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
		return 0, fmt.Errorf("%w: NATS_JS_RETENTION: unknown retention policy %q", ErrConfig, s)
	}
}

// CheckStreams — JetStream сервера и стрим, который накрывает каждый filter целиком
// (StreamChecker): сервер без JetStream на публикацию отвечает «нет ответа от стрима», как
// subject без стрима, а информацию аккаунта не отдаёт.
func (b *jsBus) CheckStreams(ctx context.Context, filters ...string) error {
	if _, err := b.js.AccountInfo(ctx); err != nil {
		if errors.Is(err, jetstream.ErrJetStreamNotEnabled) || errors.Is(err, jetstream.ErrJetStreamNotEnabledForAccount) {
			return fmt.Errorf("%w: server: %w", ErrJetStreamDisabled, err)
		}
		return fmt.Errorf("natskit: jetstream account info: %w", err)
	}
	for _, filter := range filters {
		if err := b.covered(ctx, filter); err != nil {
			return err
		}
	}
	return nil
}

// covered: сервер отбирает стримы, чьи subject'ы пересекаются с filter, а накрыть его целиком
// должен subject одного из них — пересечения мало (`user.events.a` пересекает `user.>`).
func (b *jsBus) covered(ctx context.Context, filter string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // досрочный выход не оставляет горутину листинга ждать читателя
	streams := b.js.ListStreams(ctx, jetstream.WithStreamListSubject(filter))
	for info := range streams.Info() {
		for _, subject := range info.Config.Subjects {
			if SubjectMatches(subject, filter) {
				return nil
			}
		}
	}
	if err := streams.Err(); err != nil {
		return fmt.Errorf("natskit: streams for %s: %w", filter, err)
	}
	return fmt.Errorf("%w: %s", ErrNoStream, filter)
}

func (b *jsBus) PublishStream(ctx context.Context, subject string, payload []byte, opts ...PublishOption) error {
	if !b.enabled {
		return ErrJetStreamDisabled
	}
	po := NewPublishSpec(opts...)
	// pubCtx несёт producer-span: измерение с ним получает exemplar на этот трейс.
	msg, pubCtx, span := b.instr.startPublishSpan(ctx, subject, payload, po)

	start := time.Now()
	_, pubErr := b.js.PublishMsg(ctx, msg, po.jetStreamOpts()...)
	b.instr.recordPublish(pubCtx, po, time.Since(start), pubErr, span)

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
// Потерянный PubAck завершается ошибкой через NATS_JS_PUBLISH_ASYNC_TIMEOUT.
// Конкурентные батчи одной шины независимы: метод ждёт только свои PubAck'и.
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
	pubCtxs := make([]context.Context, len(msgs))
	starts := make([]time.Time, len(msgs))
	opts := make([]PublishSpec, len(msgs))

	for i, m := range msgs {
		opts[i] = m.spec()
		// pubCtx несёт producer-span сообщения: измерение с ним получает exemplar.
		msg, pubCtx, span := b.instr.startPublishSpan(ctx, m.Subject, m.Payload, opts[i])
		spans[i], pubCtxs[i] = span, pubCtx
		starts[i] = time.Now()

		f, err := b.js.PublishMsgAsync(msg, opts[i].jetStreamOpts()...)
		if err != nil {
			errs[i] = err
			b.instr.recordPublish(pubCtx, opts[i], time.Since(starts[i]), err, span)
			continue
		}
		futures[i] = f
	}

	// Ожидание идёт по своим future'ам, а не по PublishAsyncComplete(): тот
	// закрывается только когда пуст общий pending всей шины, и потерянный
	// PubAck соседнего батча задерживал бы этот на весь PublishAsyncTimeout.
	var batchErr error
	for i := range futures {
		if futures[i] == nil {
			continue
		}
		if batchErr != nil {
			// Бюджет батча исчерпан: остальные future'ы читаются без блокировки.
			// Уже подтверждённый PubAck — успех: иначе потребитель ретраит запись, которая в стриме.
			errs[i] = settledResult(futures[i], ctx.Err())
			b.instr.recordPublish(pubCtxs[i], opts[i], time.Since(starts[i]), errs[i], spans[i])
			continue
		}
		select {
		case <-futures[i].Ok():
		case errs[i] = <-futures[i].Err():
		case <-ctx.Done():
			errs[i] = settledResult(futures[i], ctx.Err())
			batchErr = fmt.Errorf("natskit: jetstream batch: %w", ctx.Err())
		}
		b.instr.recordPublish(pubCtxs[i], opts[i], time.Since(starts[i]), errs[i], spans[i])
	}
	return errs, batchErr
}

// settledResult читает уже готовый исход публикации, не блокируясь; если
// PubAck ещё не пришёл — исход задаёт pending.
func settledResult(f jetstream.PubAckFuture, pending error) error {
	select {
	case <-f.Ok():
		return nil
	case err := <-f.Err():
		return err
	default:
		return pending
	}
}

// streamConsumer — координаты консьюмера: они идут в label'ы, спаны и заголовки
// DLQ-копии, чтобы мёртвое сообщение было привязано к своему консьюмеру.
type streamConsumer struct {
	stream  string
	durable string
	filter  string
}

func (b *jsBus) SubscribeStream(ctx context.Context, stream, subject, durable string, handler Handler) (Subscription, error) {
	if !b.enabled {
		return nil, ErrJetStreamDisabled
	}
	if b.dlqSubject != "" && SubjectMatches(subject, b.dlqSubject) {
		return nil, fmt.Errorf("%w: NATS_JS_DLQ_SUBJECT %q is matched by consumer filter %q: dead letters would loop back to the handler",
			ErrConfig, b.dlqSubject, subject)
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
		return nil, fmt.Errorf("natskit: jetstream consumer: %w", err)
	}
	sc := streamConsumer{stream: stream, durable: durable, filter: subject}

	return b.c.subscribe(func() (*subscription, error) {
		s := &subscription{conn: b.c, subject: subject}
		cc, err := cons.Consume(func(msg jetstream.Msg) {
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
				b.handleStreamOne(sc, msg, handler)
			}()
		}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			b.log.Warn(context.Background(), "natskit: jetstream consume error",
				"stream", stream, "subject", subject, "durable", durable, "err", err)
		}))
		if err != nil {
			return nil, fmt.Errorf("natskit: jetstream consume: %w", err)
		}
		s.done = cc.Closed()
		s.stop = func(drain bool) error {
			if drain {
				cc.Drain()
			} else {
				cc.Stop()
			}
			return nil
		}
		return s, nil
	})
}

func (b *jsBus) handleStreamOne(sc streamConsumer, msg jetstream.Msg, handler Handler) {
	subject := msg.Subject()

	// jetstream.Msg хранит header в nats.Header-форме — переиспользуем.
	natsMsg := &nats.Msg{Subject: subject, Data: msg.Data(), Header: msg.Headers()}
	// Метрики пишутся с ctx обработчика: он несёт consumer-span, без которого
	// у гистограммы нет exemplar'а в трейс этой обработки.
	ctx, span := b.instr.startConsumerSpan(b.c.handlerContext(), natsMsg, sc.filter)
	ctx, cancel := context.WithTimeout(ctx, b.handlerTimeout)
	defer cancel()
	filterAttr := b.instr.subjectAttrs(sc.filter)
	b.instr.inflight.Add(ctx, 1, filterAttr)
	defer b.instr.inflight.Add(ctx, -1, filterAttr)
	start := time.Now()

	stopProgress := b.keepInProgress(ctx, msg, subject)
	panicked, handlerErr := b.invoke(ctx, subject, handler, msg.Data())
	stopProgress()

	status := "ok"
	if handlerErr != nil {
		span.RecordError(handlerErr)
		span.SetStatus(codes.Error, handlerErr.Error())
		status = b.failStream(ctx, sc, msg, handlerErr, panicked)
	} else if err := msg.Ack(); err != nil {
		b.log.Error(ctx, "natskit: jetstream ack error", "subject", subject, "err", err)
	}

	b.instr.consumedTotal.Add(ctx, 1,
		b.instr.subjectAttrs(sc.filter, attribute.String("status", status)))
	b.instr.handleDuration.Record(ctx, time.Since(start).Seconds(), filterAttr)
	span.End()
}

// keepInProgress шлёт +WPI каждые AckWait/2, пока обработчик работает: сервер
// не переотправляет сообщение живому, но медленному обработчику. Транзиентная
// ошибка не выключает keepalive — иначе ределивери придёт живому обработчику.
func (b *jsBus) keepInProgress(ctx context.Context, msg jetstream.Msg, subject string) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(max(b.ackWait/2, minInProgressInterval))
		defer t.Stop()
		warned := false
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				err := msg.InProgress()
				if err == nil {
					continue
				}
				if errors.Is(err, jetstream.ErrMsgAlreadyAckd) {
					return
				}
				if !warned {
					warned = true
					b.log.Warn(ctx, "natskit: jetstream in-progress", "subject", subject, "err", err)
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (b *jsBus) invoke(ctx context.Context, subject string, handler Handler, data []byte) (panicked bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("natskit: jetstream handler panic: %v", rec)
			panicked = true
			b.log.Error(
				ctx, "natskit: jetstream handler panic",
				"subject", subject, "panic", rec, "stack", string(debug.Stack()),
			)
		}
	}()
	return false, handler(ctx, data)
}

// failStream решает судьбу неуспешного сообщения: терминальная ошибка или
// исчерпанный MaxDeliver — DLQ (если задан) + Term; иначе NAK с растущей задержкой.
// Term делается только после успешной DLQ-публикации: иначе недоступный DLQ
// стирал бы сообщение и из стрима, и из DLQ. Провал публикации оставляет
// сообщение на ределивери (NAK) — попытки ограничены MaxDeliver на сервере.
func (b *jsBus) failStream(ctx context.Context, sc streamConsumer, msg jetstream.Msg, cause error, panicked bool) string {
	subject := msg.Subject()
	deliveries, streamSeq := position(msg)
	if reason := b.terminalReason(cause, deliveries); reason != "" {
		if err := b.deadLetter(ctx, sc, msg, cause, reason, deliveries); err != nil {
			b.log.Error(ctx, "natskit: jetstream dlq publish failed, message kept for redelivery",
				"stream", sc.stream, "durable", sc.durable, "subject", subject, "stream_seq", streamSeq,
				"deliveries", deliveries, "reason", reason, "dlq", b.dlqSubject,
				"err", err, "cause", cause)
			b.nak(ctx, msg, subject, deliveries)
			return "dlq_error"
		}
		b.log.Error(ctx, "natskit: jetstream message dead",
			"stream", sc.stream, "durable", sc.durable, "subject", subject, "stream_seq", streamSeq,
			"deliveries", deliveries, "reason", reason, "dlq", b.dlqSubject, "err", cause)
		if err := msg.TermWithReason(truncate(headerValue(cause.Error()), maxTermReasonLen)); err != nil {
			b.log.Error(ctx, "natskit: jetstream term error", "subject", subject, "err", err)
		}
		return "dead"
	}

	b.nak(ctx, msg, subject, deliveries)
	if !panicked && !errors.Is(cause, context.Canceled) {
		b.log.Error(ctx, "natskit: jetstream handler error", "subject", subject, "err", cause)
	}
	if panicked {
		return "panic"
	}
	return "handler_error"
}

// nak возвращает сообщение в очередь ределивери; ошибка самого NAK не меняет
// судьбу сообщения (сервер переотправит его по AckWait), но не должна теряться.
func (b *jsBus) nak(ctx context.Context, msg jetstream.Msg, subject string, deliveries uint64) {
	var err error
	if delay := b.nakDelay(deliveries); delay > 0 {
		err = msg.NakWithDelay(delay)
	} else {
		err = msg.Nak()
	}
	if err != nil {
		b.log.Warn(ctx, "natskit: jetstream nak error", "subject", subject, "err", err)
	}
}

// nakMaxBackoff — потолок задержки NAK: потолок ниже NakBackoff (в том числе ноль) даёт
// постоянную задержку, а не сжимает её до потолка.
func nakMaxBackoff(j JetStreamConfig) time.Duration {
	return max(j.NakMaxBackoff, j.NakBackoff)
}

// nakDelay — задержка ределивери после n-й доставки: NakBackoff, удвоенная на каждую
// следующую доставку, не больше потолка: короткий сбой — ретраи через секунды, долгий —
// не чаще раза в потолок.
func (b *jsBus) nakDelay(n uint64) time.Duration {
	d := b.nakBackoff
	for i := uint64(1); i < n && d > 0 && d < b.nakMaxBackoff; i++ {
		d *= 2
	}
	return min(d, b.nakMaxBackoff)
}

// terminalReason — почему сообщение не пойдёт на ретрай; пусто — идёт.
func (b *jsBus) terminalReason(cause error, deliveries uint64) string {
	if errors.Is(cause, ErrTerminal) {
		return ReasonTerminal
	}
	if b.maxDeliver > 0 && deliveries >= uint64(b.maxDeliver) {
		return ReasonMaxDeliver
	}
	return ""
}

// position — номер доставки сообщения и его sequence в стриме: по нему мёртвое
// сообщение достают из стрима руками.
func position(msg jetstream.Msg) (deliveries, streamSeq uint64) {
	md, err := msg.Metadata()
	if err != nil {
		return 0, 0
	}
	return md.NumDelivered, md.Sequence.Stream
}

// deadLetter публикует копию мёртвого сообщения в DLQSubject и сообщает исход:
// ошибка означает, что копии нет и оригинал терять нельзя. Публикация идёт
// через JetStream (PubAck подтверждает персист); если subject не накрыт стримом,
// копия уходит Core-публикацией — DLQ остаётся диагностическим каналом.
func (b *jsBus) deadLetter(ctx context.Context, sc streamConsumer, msg jetstream.Msg, cause error, reason string, deliveries uint64) error {
	if b.dlqSubject == "" {
		return nil
	}
	dlq := &nats.Msg{Subject: b.dlqSubject, Data: msg.Data(), Header: b.dlqHeaders(sc, msg, cause, reason, deliveries)}

	// Собственный бюджет: ctx обработчика к этому моменту может быть отменён по HandlerTimeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dlqPublishTimeout)
	defer cancel()

	var err error
	if b.dlqInStream {
		_, err = b.js.PublishMsg(ctx, dlq, jetstream.WithRetryAttempts(0))
	}
	// Стрим мог исчезнуть после старта — копия всё равно уходит, но без PubAck.
	if !b.dlqInStream || errors.Is(err, jetstream.ErrNoStreamResponse) {
		if err = b.conn.PublishMsg(dlq); err == nil {
			// Core-публикация возвращается сразу: без flush отказ соединения
			// остался бы незамеченным, и оригинал ушёл бы в Term.
			err = b.conn.FlushWithContext(ctx)
		}
	}
	if err != nil {
		return fmt.Errorf("natskit: dlq publish: %w", err)
	}
	return nil
}

func (b *jsBus) dlqHeaders(sc streamConsumer, msg jetstream.Msg, cause error, reason string, deliveries uint64) nats.Header {
	h := nats.Header{}
	for k, v := range msg.Headers() {
		// Nats-Expected-* адресованы исходному стриму: в DLQ они молча отвергнут копию.
		if strings.HasPrefix(k, "Nats-Expected-") {
			continue
		}
		h[k] = slices.Clone(v)
	}
	// С Nats-Msg-Id оригинала стрим, накрывающий DLQ-subject, счёл бы DLQ-копию дублем и молча выбросил.
	if id := h.Get(jetstream.MsgIDHeader); id != "" {
		h.Del(jetstream.MsgIDHeader)
		h.Set(HeaderDLQMsgID, id)
	}
	h.Set(HeaderDLQOrigin, msg.Subject())
	h.Set(HeaderDLQError, headerValue(cause.Error()))
	h.Set(HeaderDLQReason, reason)
	h.Set(HeaderDLQStream, sc.stream)
	h.Set(HeaderDLQConsumer, sc.durable)
	h.Set(HeaderDLQDeliveries, strconv.FormatUint(deliveries, 10))
	h.Set(HeaderDLQInstance, b.instance)
	return h
}

// headerValue убирает переводы строк: они ломают header-блок протокола NATS.
func headerValue(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

// SubjectMatches сообщает, накрывает ли filter (с wildcard'ами `*` и `>`) subject — та же
// семантика, что у фильтра подписки NATS. subject — конкретный или сам фильтр: тогда true,
// только если filter накрывает каждый подходящий под него subject.
func SubjectMatches(filter, subject string) bool {
	f := strings.Split(filter, ".")
	s := strings.Split(subject, ".")
	for i, token := range f {
		if token == ">" {
			return i < len(s)
		}
		if i >= len(s) || s[i] == ">" {
			return false
		}
		if token != "*" && token != s[i] {
			return false
		}
	}
	return len(f) == len(s)
}

// PublishStreamJSON маршалит msg в JSON и публикует его в стрим.
func PublishStreamJSON[T any](b StreamBus, ctx context.Context, subject string, msg T, opts ...PublishOption) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natskit: marshal payload: %w", err)
	}
	return b.PublishStream(ctx, subject, data, opts...)
}

// SubscribeStreamJSON — durable-подписка с декодом payload в T; ошибка декода
// терминальна (Term/DLQ вместо бесконечного ретрая).
func SubscribeStreamJSON[T any](b StreamBus, ctx context.Context, stream, subject, durable string, h func(ctx context.Context, msg T) error) (Subscription, error) {
	return b.SubscribeStream(ctx, stream, subject, durable, decodeJSON(h))
}
