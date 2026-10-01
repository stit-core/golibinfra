package natskit_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestStreamBus_Disabled(t *testing.T) {
	cfg := natskit.Config{}
	conn := natskit.ExportConn(&natsgo.Conn{})
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus disabled: %v", err)
	}
	if err := sb.PublishStream(context.Background(), "x.y", []byte("{}")); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("PublishStream err = %v, want ErrJetStreamDisabled", err)
	}
	if _, err := sb.PublishStreamBatch(context.Background(), []natskit.StreamMsg{{Subject: "x.y", Payload: []byte("{}")}}); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("PublishStreamBatch err = %v, want ErrJetStreamDisabled", err)
	}
	if _, err := sb.SubscribeStream(context.Background(), "S", "x.y", "d", func(context.Context, []byte) error { return nil }); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("SubscribeStream err = %v, want ErrJetStreamDisabled", err)
	}
	if err := sb.(natskit.StreamChecker).CheckStreams(context.Background()); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("CheckStreams err = %v, want ErrJetStreamDisabled", err)
	}
}

// Публикатор узнаёт на старте, что его subject'ы не попадут в стрим: пересечения со
// стримом мало, фильтр накрыт целиком или это ErrNoStream.
func TestStreamBus_CheckStreams(t *testing.T) {
	stream := nameFor(t)
	checker := newStreamBus(t, jsConfig(stream)).(natskit.StreamChecker)
	ctx := context.Background()
	for _, filter := range []string{stream + ".>", stream + ".events.*", subj(t, "created")} {
		if err := checker.CheckStreams(ctx, filter); err != nil {
			t.Errorf("CheckStreams(%s) = %v, want covered", filter, err)
		}
	}
	for _, filter := range []string{stream, stream + "_other.>", ">"} {
		if err := checker.CheckStreams(ctx, filter); !errors.Is(err, natskit.ErrNoStream) || !strings.Contains(err.Error(), filter) {
			t.Errorf("CheckStreams(%s) = %v, want ErrNoStream naming the filter", filter, err)
		}
	}
	if err := checker.CheckStreams(ctx); err != nil {
		t.Errorf("CheckStreams() with JetStream enabled = %v", err)
	}
}

// Невалидный конфиг не доживает до первой публикации.
func TestNewStreamBus_RejectsInvalidConfig(t *testing.T) {
	cfg := jsConfig("ORDERS")
	cfg.JetStream.DLQSubject = "dlq orders"
	if _, err := natskit.NewStreamBus(natskit.ExportConn(&natsgo.Conn{}), logkit.Nop(), cfg); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewStreamBus err = %v, want ErrConfig", err)
	}
}

func TestStreamBus_WildcardSubjectInContext(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))

	gotSubject := make(chan string, 1)
	if _, err := sb.SubscribeStream(context.Background(), stream, stream+".>", "wild", func(ctx context.Context, _ []byte) error {
		gotSubject <- natskit.SubjectFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), subj(t, "created"), []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if s := recv(t, gotSubject, 10*time.Second, "jetstream message"); s != subj(t, "created") {
		t.Fatalf("SubjectFromContext = %q, want %q", s, subj(t, "created"))
	}
}

func TestStreamBus_PublishSubscribe(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	subject := subj(t, "created")

	type Msg struct {
		ID int `json:"id"`
	}
	got := make(chan Msg, 1)
	if _, err := natskit.SubscribeStreamJSON(sb, context.Background(), stream, subject, "worker", func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := natskit.PublishStreamJSON(sb, context.Background(), subject, Msg{ID: 7}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if m := recv(t, got, 10*time.Second, "jetstream message"); m.ID != 7 {
		t.Fatalf("want id=7, got %d", m.ID)
	}
}

// Прикладные заголовки публикации доезжают до обработчика вместе с сообщением.
func TestStreamBus_PublishHeadersReachHandler(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	subject := subj(t, "created")

	got := make(chan natsgo.Header, 1)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "worker", func(ctx context.Context, _ []byte) error {
		got <- natskit.HeadersFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	err := sb.PublishStream(context.Background(), subject, []byte(`{}`),
		natskit.WithHeader("Event-Type", "order.created"),
		natskit.WithMsgID("evt-1"),
		natskit.WithMetricSubject(stream+".*"),
	)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, got, 10*time.Second, "jetstream message")
	if v := h.Get("Event-Type"); v != "order.created" {
		t.Fatalf("Event-Type = %q", v)
	}
	if v := h.Get(jetstream.MsgIDHeader); v != "evt-1" {
		t.Fatalf("%s = %q", jetstream.MsgIDHeader, v)
	}
}

func TestStreamBus_PublishBatchDedup(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.Duplicates = time.Minute
	sb := newStreamBus(t, cfg)
	subject := subj(t, "created")

	type order struct {
		ID int `json:"id"`
	}
	got := make(chan int, 8)
	if _, err := natskit.SubscribeStreamJSON(sb, context.Background(), stream, subject, "batch",
		func(_ context.Context, m order) error {
			got <- m.ID
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payload := func(id int) []byte { return fmt.Appendf(nil, `{"id":%d}`, id) }
	batch := []natskit.StreamMsg{
		{Subject: subject, Payload: payload(1), MsgID: "1"},
		{Subject: subject, Payload: payload(1), MsgID: "1"}, // дубль по MsgID — стрим не сохранит
		{Subject: subject, Payload: payload(2), MsgID: "2", Header: natsgo.Header{"Event-Type": []string{"created"}}},
	}
	results, err := sb.PublishStreamBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("PublishStreamBatch: %v", err)
	}
	if len(results) != len(batch) {
		t.Fatalf("results len = %d, want %d", len(results), len(batch))
	}
	for i, e := range results {
		if e != nil {
			t.Fatalf("results[%d] = %v, want nil (дубль — это PubAck, не ошибка)", i, e)
		}
	}

	seen := map[int]int{}
	deadline := time.After(10 * time.Second)
	for len(seen) < 2 {
		select {
		case id := <-got:
			seen[id]++
		case <-deadline:
			t.Fatalf("timeout; seen=%v, want {1:1, 2:1}", seen)
		}
	}
	expectSilence(t, got, time.Second, "extra delivery (dedup failed)")
	if seen[1] != 1 || seen[2] != 1 {
		t.Fatalf("seen = %v, want {1:1, 2:1}", seen)
	}
}

// Отменённый ctx батча: сбой всего батча, но уже подтверждённые сообщения не
// помечаются ошибкой (иначе потребитель ретраит запись, которая в стриме).
func TestStreamBus_PublishBatchCancelledContext(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	subject := subj(t, "created")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := sb.PublishStreamBatch(ctx, []natskit.StreamMsg{
		{Subject: subject, Payload: []byte(`{"id":1}`), MsgID: "1"},
		{Subject: subject, Payload: []byte(`{"id":2}`), MsgID: "2"},
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("PublishStreamBatch err = %v, want context.Canceled", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	for i, e := range results {
		if e != nil && !errors.Is(e, context.Canceled) {
			t.Fatalf("results[%d] = %v, want nil or context.Canceled", i, e)
		}
	}
}

func TestStreamBus_MaxDeliverDLQ(t *testing.T) {
	stream := nameFor(t)
	log := &memLogger{}
	cfg := jsConfig(stream)
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.MaxDeliver = 3
	cfg.JetStream.DLQSubject = "DLQ." + stream
	conn := connect(t, natsURL(t))
	sb, err := natskit.NewStreamBus(conn, log, cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	coreBus := natskit.NewBus(conn, logkit.Nop(), cfg)
	subject := subj(t, "created")

	// DLQ-консьюмер (core): принимает «мёртвое» сообщение и читает заголовки.
	dlq := make(chan natsgo.Header, 1)
	if _, err := coreBus.Subscribe(context.Background(), cfg.JetStream.DLQSubject, func(ctx context.Context, _ []byte) error {
		dlq <- natskit.HeadersFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}

	attempts := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "poison",
		func(context.Context, []byte) error {
			attempts <- struct{}{}
			return errors.New("boom\nsecond line")
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`), natskit.WithMsgID("evt-1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 30*time.Second, "DLQ message")
	if got := h.Get(natskit.HeaderDLQOrigin); got != subject {
		t.Fatalf("DLQ origin = %q, want %q", got, subject)
	}
	if got := h.Get(natskit.HeaderDLQError); got != "boom second line" {
		t.Fatalf("DLQ error header = %q, want newline-free text", got)
	}
	if got := h.Get(natskit.HeaderDLQStream); got != stream {
		t.Fatalf("%s = %q, want %q", natskit.HeaderDLQStream, got, stream)
	}
	if got := h.Get(natskit.HeaderDLQConsumer); got != "poison" {
		t.Fatalf("%s = %q, want poison", natskit.HeaderDLQConsumer, got)
	}
	if got := h.Get(natskit.HeaderDLQDeliveries); got != "3" {
		t.Fatalf("%s = %q, want 3", natskit.HeaderDLQDeliveries, got)
	}
	if got := h.Get(natskit.HeaderDLQReason); got != natskit.ReasonMaxDeliver {
		t.Fatalf("%s = %q, want %s", natskit.HeaderDLQReason, got, natskit.ReasonMaxDeliver)
	}
	if got := h.Get(natskit.HeaderDLQMsgID); got != "evt-1" {
		t.Fatalf("%s = %q, want evt-1", natskit.HeaderDLQMsgID, got)
	}

	// Причина последней попытки — в логе, а не только в заголовке DLQ-копии.
	eventually(t, 5*time.Second, "dead message logged", func() bool {
		return log.has("error", "natskit: jetstream message dead")
	})
	if e := log.find("error", "natskit: jetstream message dead"); e == nil || !strings.Contains(e.text(), "boom") {
		t.Fatalf("dead-message log has no cause: %v", log.dump())
	}

	// после Term ределивери прекращается — попыток ровно MaxDeliver.
	for range 3 {
		recv(t, attempts, 20*time.Second, "handler attempt")
	}
	expectSilence(t, attempts, 3*time.Second, "delivery after Term")
}

// Без DLQ-subject мёртвое сообщение всё равно логируется и получает Term.
func TestStreamBus_TermWithoutDLQ(t *testing.T) {
	stream := nameFor(t)
	log := &memLogger{}
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.MaxDeliver = 2
	sb, err := natskit.NewStreamBus(connect(t, natsURL(t)), log, cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	subject := subj(t, "created")

	attempts := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "poison",
		func(context.Context, []byte) error {
			attempts <- struct{}{}
			return errors.New("boom")
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	for range 2 {
		recv(t, attempts, 20*time.Second, "handler attempt")
	}
	expectSilence(t, attempts, 3*time.Second, "delivery after Term")
	if !log.has("error", "natskit: jetstream message dead") {
		t.Fatalf("silent drop is not logged: %v", log.dump())
	}
}

// ErrTerminal — «не ретраить»: DLQ и Term с первой попытки.
func TestStreamBus_TerminalErrorSkipsRetries(t *testing.T) {
	stream := nameFor(t)
	log := &memLogger{}
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = 5 * time.Second
	cfg.JetStream.MaxDeliver = 10
	cfg.JetStream.DLQSubject = "DLQ." + stream
	conn := connect(t, natsURL(t))
	sb, err := natskit.NewStreamBus(conn, log, cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	subject := subj(t, "created")

	dlq := make(chan natsgo.Header, 1)
	if _, err := natskit.NewBus(conn, logkit.Nop(), cfg).Subscribe(context.Background(), cfg.JetStream.DLQSubject,
		func(ctx context.Context, _ []byte) error {
			dlq <- natskit.HeadersFromContext(ctx)
			return nil
		}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}

	attempts := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "terminal",
		func(context.Context, []byte) error {
			attempts <- struct{}{}
			return fmt.Errorf("%w: unknown schema", natskit.ErrTerminal)
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 20*time.Second, "DLQ message")
	if got := h.Get(natskit.HeaderDLQReason); got != natskit.ReasonTerminal {
		t.Fatalf("%s = %q, want %s", natskit.HeaderDLQReason, got, natskit.ReasonTerminal)
	}
	if got := h.Get(natskit.HeaderDLQDeliveries); got != "1" {
		t.Fatalf("%s = %q, want 1 (без ретраев)", natskit.HeaderDLQDeliveries, got)
	}
	recv(t, attempts, time.Second, "handler attempt")
	select {
	case <-attempts:
		t.Fatalf("terminal failure was retried: %v", log.dump())
	case <-time.After(7 * time.Second): // > AckWait: ределивери пришёл бы здесь
	}
}

// Битый JSON не станет валидным при ределивери: сообщение уходит в DLQ сразу.
func TestStreamBus_DecodeErrorGoesToDLQ(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.MaxDeliver = 10
	cfg.JetStream.DLQSubject = "DLQ." + stream
	conn := connect(t, natsURL(t))
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	subject := subj(t, "created")

	dlq := make(chan natsgo.Header, 1)
	if _, err := natskit.NewBus(conn, logkit.Nop(), cfg).Subscribe(context.Background(), cfg.JetStream.DLQSubject,
		func(ctx context.Context, _ []byte) error {
			dlq <- natskit.HeadersFromContext(ctx)
			return nil
		}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}

	type Msg struct {
		ID int `json:"id"`
	}
	if _, err := natskit.SubscribeStreamJSON(sb, context.Background(), stream, subject, "decode",
		func(context.Context, Msg) error { return nil }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`not json`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 20*time.Second, "DLQ message")
	if got := h.Get(natskit.HeaderDLQReason); got != natskit.ReasonTerminal {
		t.Fatalf("%s = %q, want %s", natskit.HeaderDLQReason, got, natskit.ReasonTerminal)
	}
}

// Паника обработчика идёт по poison-пути: NAK, ределивери, затем DLQ/Term.
func TestStreamBus_PanicGoesPoisonPath(t *testing.T) {
	stream := nameFor(t)
	log := &memLogger{}
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.MaxDeliver = 2
	sb, err := natskit.NewStreamBus(connect(t, natsURL(t)), log, cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	subject := subj(t, "created")

	attempts := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "panic",
		func(context.Context, []byte) error {
			attempts <- struct{}{}
			panic("handler exploded")
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	recv(t, attempts, 20*time.Second, "first attempt")
	recv(t, attempts, 20*time.Second, "redelivery after panic")
	expectSilence(t, attempts, 3*time.Second, "delivery after Term")
	if !log.has("error", "natskit: jetstream handler panic") {
		t.Fatalf("panic not logged: %v", log.dump())
	}
}

// NAK с backoff разносит попытки во времени, а не крутит горячую петлю.
func TestStreamBus_NakBackoffDelaysRedelivery(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = 30 * time.Second
	cfg.JetStream.MaxDeliver = 3
	cfg.JetStream.NakBackoff = time.Second
	sb := newStreamBus(t, cfg)
	subject := subj(t, "created")

	attempts := make(chan time.Time, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "backoff",
		func(context.Context, []byte) error {
			attempts <- time.Now()
			return errors.New("boom")
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	first := recv(t, attempts, 20*time.Second, "first attempt")
	second := recv(t, attempts, 20*time.Second, "redelivery")
	if gap := second.Sub(first); gap < 900*time.Millisecond {
		t.Fatalf("redelivery gap = %v, want ≈NakBackoff (1s)", gap)
	}
}

// DLQ-subject, накрытый фильтром консьюмера, зациклил бы poison-сообщение —
// подписка отвергается на регистрации.
func TestStreamBus_RejectsDLQUnderConsumerFilter(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.DLQSubject = stream + ".dlq"
	sb := newStreamBus(t, cfg)

	_, err := sb.SubscribeStream(context.Background(), stream, stream+".>", "loop",
		func(context.Context, []byte) error { return nil })
	if !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("SubscribeStream err = %v, want ErrConfig", err)
	}
}

// DLQ-subject внутри стрима с окном дедупа: копия сохраняется, Nats-Msg-Id
// оригинала снят (иначе стрим счёл бы копию дублем). Читает DLQ отдельный
// консьюмер — со своим конфигом без DLQSubject.
func TestStreamBus_DLQInsideStreamWithDedup(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.Duplicates = time.Minute
	cfg.JetStream.AckWait = time.Second
	cfg.JetStream.MaxDeliver = 2
	cfg.JetStream.DLQSubject = stream + ".dlq"
	sb := newStreamBus(t, cfg)
	subject := subj(t, "created")

	readerCfg := jsConfig(stream)
	readerCfg.JetStream.Duplicates = time.Minute
	reader := newStreamBus(t, readerCfg)

	dlq := make(chan natsgo.Header, 1)
	if _, err := reader.SubscribeStream(context.Background(), stream, cfg.JetStream.DLQSubject, "dlq-reader", func(ctx context.Context, _ []byte) error {
		dlq <- natskit.HeadersFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "poison", func(context.Context, []byte) error {
		return errors.New("boom")
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`), natskit.WithMsgID("evt-1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 30*time.Second, "DLQ message stored in stream")
	if id := h.Get(jetstream.MsgIDHeader); id != "" {
		t.Fatalf("DLQ copy keeps Nats-Msg-Id %q", id)
	}
	if got := h.Get(natskit.HeaderDLQMsgID); got != "evt-1" {
		t.Fatalf("%s = %q, want evt-1", natskit.HeaderDLQMsgID, got)
	}
	if got := h.Get(natskit.HeaderDLQOrigin); got != subject {
		t.Fatalf("origin = %q", got)
	}
}

// Недоступный DLQ не должен стирать оригинал: без успешной копии Term не
// делается, сообщение остаётся в стриме и уходит на ределивери.
func TestStreamBus_DLQPublishFailureKeepsMessage(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	dlqStream, dlqSubject := stream+"_DLQ", "dlq."+stream
	ctx := context.Background()

	// Стрим на DLQ-subject с discard=new и потолком в одно сообщение: после
	// заполнения сервер отвергает публикацию кита — воспроизводимый отказ DLQ.
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     dlqStream,
		Subjects: []string{dlqSubject},
		Storage:  jetstream.MemoryStorage,
		MaxMsgs:  1,
		Discard:  jetstream.DiscardNew,
	}); err != nil {
		t.Fatalf("create dlq stream: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), dlqStream) })
	if _, err := js.Publish(ctx, dlqSubject, []byte("filler")); err != nil {
		t.Fatalf("fill dlq stream: %v", err)
	}

	log := &memLogger{}
	cfg := jsConfig(stream)
	cfg.JetStream.AckWait = time.Second
	cfg.JetStream.NakBackoff = 100 * time.Millisecond
	cfg.JetStream.DLQSubject = dlqSubject
	sb, err := natskit.NewStreamBus(connect(t, url), log, cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	subject := subj(t, "created")

	attempts := make(chan struct{}, 32)
	if _, err := sb.SubscribeStream(ctx, stream, subject, "poison",
		func(context.Context, []byte) error {
			select {
			case attempts <- struct{}{}:
			default:
			}
			return fmt.Errorf("%w: broken payload", natskit.ErrTerminal)
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Терминальная ошибка обычно означает Term с первой попытки; с провалившимся
	// DLQ сообщение обязано вернуться на ределивери.
	recv(t, attempts, 30*time.Second, "first attempt")
	recv(t, attempts, 30*time.Second, "redelivery after a failed DLQ publish")
	eventually(t, 5*time.Second, "failed DLQ publish logged", func() bool {
		return log.has("error", "natskit: jetstream dlq publish failed, message kept for redelivery")
	})
	if log.has("error", "natskit: jetstream message dead") {
		t.Fatalf("message terminated despite a failed DLQ publish: %v", log.dump())
	}
}

// Медленный, но живой обработчик держит сообщение через +WPI: сервер не
// переотправляет его, пока не истёк HandlerTimeout.
func TestStreamBus_SlowHandlerNotRedelivered(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.AckWait = time.Second
	cfg.JetStream.HandlerTimeout = 10 * time.Second
	sb := newStreamBus(t, cfg)
	subject := subj(t, "slow")

	calls := make(chan struct{}, 4)
	done := make(chan error, 4)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "slow", func(ctx context.Context, _ []byte) error {
		calls <- struct{}{}
		select {
		case <-time.After(2500 * time.Millisecond):
			done <- nil
			return nil
		case <-ctx.Done():
			done <- ctx.Err()
			return ctx.Err()
		}
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	recv(t, calls, 10*time.Second, "handler call")
	if err := recv(t, done, 10*time.Second, "handler"); err != nil {
		t.Fatalf("handler cancelled: %v", err)
	}
	// окно, в котором сервер переотправил бы сообщение без +WPI
	expectSilence(t, calls, 2*time.Second, "redelivery to a live handler")
}

// Зависший обработчик получает отмену ctx по HandlerTimeout, сообщение уходит
// на ределивери через NAK.
func TestStreamBus_StuckHandlerCancelled(t *testing.T) {
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.HandlerTimeout = 500 * time.Millisecond
	sb := newStreamBus(t, cfg)
	subject := subj(t, "stuck")

	var attempt atomic.Int32
	results := make(chan error, 4)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "stuck", func(ctx context.Context, _ []byte) error {
		if attempt.Add(1) == 1 {
			<-ctx.Done()
			results <- ctx.Err()
			return ctx.Err()
		}
		results <- nil
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := sb.PublishStream(context.Background(), subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	start := time.Now()
	if err := recv(t, results, 5*time.Second, "first attempt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first attempt err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancel took %v, want ≈HandlerTimeout", elapsed)
	}
	if err := recv(t, results, 10*time.Second, "redelivery"); err != nil {
		t.Fatalf("second attempt err = %v", err)
	}
}

func TestStreamBus_PublishBatchPartialError(t *testing.T) {
	stream := nameFor(t)
	sb := newStreamBus(t, jsConfig(stream))
	subject := subj(t, "created")

	delivered := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), stream, subject, "partial",
		func(context.Context, []byte) error {
			delivered <- struct{}{}
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	oversized := make([]byte, 2<<20) // > server max_payload (1 MiB) → ErrMaxPayload
	batch := []natskit.StreamMsg{
		{Subject: subject, Payload: []byte(`{"id":1}`)},
		{Subject: subject, Payload: oversized},
		{Subject: subject, Payload: []byte(`{"id":3}`)},
	}
	results, err := sb.PublishStreamBatch(context.Background(), batch)
	if err != nil {
		t.Fatalf("PublishStreamBatch: %v", err)
	}
	if results[0] != nil || results[2] != nil {
		t.Fatalf("валидные сообщения зафейлились: results=%v", results)
	}
	if results[1] == nil {
		t.Fatal("oversized должно было зафейлиться (results[1] == nil)")
	}

	for range 2 {
		recv(t, delivered, 10*time.Second, "valid deliveries")
	}
	expectSilence(t, delivered, time.Second, "delivery of oversized message")
}

// Потерянный PubAck завершается ErrAsyncPublishTimeout, а не висит вечно.
func TestStreamBus_PublishBatchLostAckTimesOut(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	proxy := startBlackholeProxy(t, strings.TrimPrefix(url, "nats://"))
	nc, err := natsgo.Connect("nats://" + proxy.addr())
	if err != nil {
		t.Fatalf("connect via proxy: %v", err)
	}
	t.Cleanup(func() {
		proxy.drop.Store(false)
		nc.Close()
	})
	cfg := jsConfig(stream)
	cfg.JetStream.PublishAsyncTimeout = 500 * time.Millisecond
	sb, err := natskit.NewStreamBus(natskit.ExportConn(nc), logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	proxy.drop.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	results, err := sb.PublishStreamBatch(ctx, []natskit.StreamMsg{{Subject: subj(t, "created"), Payload: []byte(`{}`)}})
	if err != nil {
		t.Fatalf("PublishStreamBatch: %v", err)
	}
	if !errors.Is(results[0], jetstream.ErrAsyncPublishTimeout) {
		t.Fatalf("results[0] = %v, want ErrAsyncPublishTimeout", results[0])
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("batch took %v, want ≈PublishAsyncTimeout", elapsed)
	}
}

func rawJetStream(t *testing.T, url string) jetstream.JetStream {
	t.Helper()
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return js
}

func streamConfig(t *testing.T, js jetstream.JetStream, name string) jetstream.StreamConfig {
	t.Helper()
	s, err := js.Stream(context.Background(), name)
	if err != nil {
		t.Fatalf("stream %s: %v", name, err)
	}
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	return info.Config
}

// Существующий стрим: кит доливает только свой subject, а настройки оператора
// (MaxBytes, Duplicates, Description, чужие subjects) не трогает.
func TestStreamBus_EnsureStreamsKeepsOperatorTuning(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	js := rawJetStream(t, url)
	tuned := jetstream.StreamConfig{
		Name:        stream,
		Description: "tuned by operator",
		Subjects:    []string{"EXTRA_" + stream + ".>"},
		MaxBytes:    1 << 20,
		Duplicates:  30 * time.Second,
		Storage:     jetstream.FileStorage,
	}
	if _, err := js.CreateStream(context.Background(), tuned); err != nil {
		t.Fatalf("create tuned stream: %v", err)
	}

	for i := range 2 { // второй старт — идемпотентен
		if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), jsConfig(stream)); err != nil {
			t.Fatalf("NewStreamBus #%d: %v", i, err)
		}
	}

	got := streamConfig(t, js, stream)
	if !slices.Contains(got.Subjects, tuned.Subjects[0]) || !slices.Contains(got.Subjects, stream+".>") || len(got.Subjects) != 2 {
		t.Fatalf("subjects = %v, want union", got.Subjects)
	}
	if got.MaxBytes != tuned.MaxBytes || got.Duplicates != tuned.Duplicates || got.Description != tuned.Description || got.MaxAge != 0 {
		t.Fatalf("operator tuning clobbered: MaxBytes=%d Duplicates=%v Description=%q MaxAge=%v",
			got.MaxBytes, got.Duplicates, got.Description, got.MaxAge)
	}
}

// Расхождение конфига стрима с ENV не применяется молча: оператор видит warn.
func TestStreamBus_WarnsOnStreamConfigDrift(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	js := rawJetStream(t, url)
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{stream + ".>"},
		MaxAge:   4 * time.Hour,
		Storage:  jetstream.FileStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}

	log := &memLogger{}
	if _, err := natskit.NewStreamBus(connect(t, url), log, jsConfig(stream)); err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	entry := log.find("warn", "natskit: stream config drift")
	if entry == nil {
		t.Fatalf("drift not reported: %v", log.dump())
	}
	if !strings.Contains(entry.text(), "max_age") {
		t.Fatalf("drift entry does not name the field: %s", entry.text())
	}
}

func TestStreamBus_EnsureStreamsCreatesMissing(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.Duplicates = 30 * time.Second
	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	got := streamConfig(t, rawJetStream(t, url), stream)
	if !slices.Equal(got.Subjects, []string{stream + ".>"}) || got.MaxAge != time.Hour || got.Duplicates != 30*time.Second {
		t.Fatalf("created stream config = %+v", got)
	}
}

// StreamManaged=false: кит не создаёт и не меняет стримы, но требует их наличия.
func TestStreamBus_UnmanagedStreams(t *testing.T) {
	url := natsURL(t)
	stream := nameFor(t)
	cfg := jsConfig(stream)
	cfg.JetStream.StreamManaged = false

	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err == nil {
		t.Fatal("want error: unmanaged stream is missing")
	}

	js := rawJetStream(t, url)
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{Name: stream, Subjects: []string{"EXTRA_" + stream + ".>"}}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err != nil {
		t.Fatalf("NewStreamBus with existing unmanaged stream: %v", err)
	}
	if got := streamConfig(t, js, stream); !slices.Equal(got.Subjects, []string{"EXTRA_" + stream + ".>"}) {
		t.Fatalf("unmanaged stream modified: subjects = %v", got.Subjects)
	}
}
