package natskit_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	if err := sb.PublishStream(context.Background(), "x.y", []byte("{}")); err == nil {
		t.Fatal("expected ErrJetStreamDisabled")
	}
	if _, err := sb.PublishStreamBatch(context.Background(), []natskit.StreamMsg{{Subject: "x.y", Payload: []byte("{}")}}); err == nil {
		t.Fatal("expected ErrJetStreamDisabled from batch")
	}
	if _, err := sb.SubscribeStream(context.Background(), "S", "x.y", "d", func(context.Context, []byte) error { return nil }); !errors.Is(err, natskit.ErrJetStreamDisabled) {
		t.Fatalf("SubscribeStream err = %v, want ErrJetStreamDisabled", err)
	}
}

func TestStreamBus_WildcardSubjectInContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	gotSubject := make(chan string, 1)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.>", "orders-wild", func(ctx context.Context, _ []byte) error {
		gotSubject <- natskit.SubjectFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), "ORDERS.created", []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if s := recv(t, gotSubject, 10*time.Second, "jetstream message"); s != "ORDERS.created" {
		t.Fatalf("SubjectFromContext = %q, want ORDERS.created", s)
	}
}

func TestStreamBus_PublishSubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	type Msg struct {
		ID int `json:"id"`
	}
	got := make(chan Msg, 1)
	if _, err := natskit.SubscribeStreamJSON(sb, context.Background(), "ORDERS", "ORDERS.created", "orders-worker", func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := natskit.PublishStreamJSON(sb, context.Background(), "ORDERS.created", Msg{ID: 7}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if m := recv(t, got, 10*time.Second, "jetstream message"); m.ID != 7 {
		t.Fatalf("want id=7, got %d", m.ID)
	}
}

func TestStreamBus_PublishBatchDedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.Duplicates = time.Minute
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	type order struct {
		ID int `json:"id"`
	}
	got := make(chan int, 8)
	if _, err := natskit.SubscribeStreamJSON(sb, context.Background(), "ORDERS", "ORDERS.created", "orders-batch",
		func(_ context.Context, m order) error {
			got <- m.ID
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payload := func(id int) []byte { return fmt.Appendf(nil, `{"id":%d}`, id) }
	batch := []natskit.StreamMsg{
		{Subject: "ORDERS.created", Payload: payload(1), MsgID: "1"},
		{Subject: "ORDERS.created", Payload: payload(1), MsgID: "1"}, // дубль по MsgID — стрим не сохранит
		{Subject: "ORDERS.created", Payload: payload(2), MsgID: "2"},
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

func TestStreamBus_MaxDeliverDLQ(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.AckWait = 2 * time.Second
	cfg.JetStream.MaxDeliver = 3
	cfg.JetStream.DLQSubject = "DLQ.orders"
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}
	coreBus := natskit.NewBus(conn, logkit.Nop(), cfg)

	// DLQ-консьюмер (core): принимает «мёртвое» сообщение и читает заголовки.
	dlq := make(chan natsgo.Header, 1)
	if _, err := coreBus.Subscribe(context.Background(), "DLQ.orders", func(ctx context.Context, _ []byte) error {
		dlq <- natskit.HeadersFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}

	var attempts int32
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.created", "orders-poison",
		func(context.Context, []byte) error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("boom\nsecond line")
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), "ORDERS.created", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 20*time.Second, "DLQ message")
	if h.Get(natskit.HeaderDLQOrigin) != "ORDERS.created" {
		t.Fatalf("DLQ origin = %q, want ORDERS.created", h.Get(natskit.HeaderDLQOrigin))
	}
	if got := h.Get(natskit.HeaderDLQError); got != "boom second line" {
		t.Fatalf("DLQ error header = %q, want newline-free text", got)
	}

	// после Term ределивери прекращается — попыток ровно MaxDeliver.
	time.Sleep(3 * time.Second)
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3 (MaxDeliver)", got)
	}
}

// DLQ-subject внутри стрима с окном дедупа: копия с Nats-Msg-Id оригинала была
// бы молча выброшена как дубль.
func TestStreamBus_DLQInsideStreamWithDedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.Duplicates = time.Minute
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.AckWait = time.Second
	cfg.JetStream.MaxDeliver = 2
	cfg.JetStream.DLQSubject = "ORDERS.dlq"
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	dlq := make(chan natsgo.Header, 1)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.dlq", "dlq-reader", func(ctx context.Context, _ []byte) error {
		dlq <- natskit.HeadersFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("dlq subscribe: %v", err)
	}
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.created", "poison", func(context.Context, []byte) error {
		return errors.New("boom")
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), "ORDERS.created", []byte(`{}`), natskit.WithMsgID("evt-1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, dlq, 20*time.Second, "DLQ message stored in stream")
	if id := h.Get(jetstream.MsgIDHeader); id != "" {
		t.Fatalf("DLQ copy keeps Nats-Msg-Id %q", id)
	}
	if got := h.Get(natskit.HeaderDLQMsgID); got != "evt-1" {
		t.Fatalf("%s = %q, want evt-1", natskit.HeaderDLQMsgID, got)
	}
	if got := h.Get(natskit.HeaderDLQOrigin); got != "ORDERS.created" {
		t.Fatalf("origin = %q", got)
	}
}

// Медленный, но живой обработчик держит сообщение через +WPI: сервер не
// переотправляет его, пока не истёк HandlerTimeout.
func TestStreamBus_SlowHandlerNotRedelivered(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.AckWait = time.Second
	cfg.JetStream.HandlerTimeout = 10 * time.Second
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	var calls atomic.Int32
	done := make(chan error, 4)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.slow", "slow", func(ctx context.Context, _ []byte) error {
		calls.Add(1)
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
	if err := sb.PublishStream(context.Background(), "ORDERS.slow", []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := recv(t, done, 10*time.Second, "handler"); err != nil {
		t.Fatalf("handler cancelled: %v", err)
	}
	time.Sleep(2 * time.Second) // окно, в котором сервер переотправил бы без +WPI
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler calls = %d, want 1 (no redelivery)", n)
	}
}

// Зависший обработчик получает отмену ctx по HandlerTimeout, сообщение уходит
// на ределивери через NAK.
func TestStreamBus_StuckHandlerCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.MaxInFlight = 8
	cfg.JetStream.HandlerTimeout = 500 * time.Millisecond
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	var attempt atomic.Int32
	results := make(chan error, 4)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.stuck", "stuck", func(ctx context.Context, _ []byte) error {
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
	if err := sb.PublishStream(context.Background(), "ORDERS.stuck", []byte(`{}`)); err != nil {
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
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	conn := connect(t, url)
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), jsConfig("ORDERS"))
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	delivered := make(chan struct{}, 8)
	if _, err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.created", "orders-partial",
		func(context.Context, []byte) error {
			delivered <- struct{}{}
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	oversized := make([]byte, 2<<20) // > server max_payload (1 MiB) → ErrMaxPayload
	batch := []natskit.StreamMsg{
		{Subject: "ORDERS.created", Payload: []byte(`{"id":1}`)},
		{Subject: "ORDERS.created", Payload: oversized},
		{Subject: "ORDERS.created", Payload: []byte(`{"id":3}`)},
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

	for i := 0; i < 2; i++ {
		recv(t, delivered, 10*time.Second, "valid deliveries")
	}
	expectSilence(t, delivered, time.Second, "delivery of oversized message")
}

// Потерянный PubAck завершается ErrAsyncPublishTimeout, а не висит вечно.
func TestStreamBus_PublishBatchLostAckTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	proxy := startBlackholeProxy(t, url[len("nats://"):])
	nc, err := natsgo.Connect("nats://" + proxy.addr())
	if err != nil {
		t.Fatalf("connect via proxy: %v", err)
	}
	t.Cleanup(func() {
		proxy.drop.Store(false)
		nc.Close()
	})
	cfg := jsConfig("ORDERS")
	cfg.JetStream.PublishAsyncTimeout = 500 * time.Millisecond
	sb, err := natskit.NewStreamBus(natskit.ExportConn(nc), logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	proxy.drop.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	results, err := sb.PublishStreamBatch(ctx, []natskit.StreamMsg{{Subject: "ORDERS.created", Payload: []byte(`{}`)}})
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
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	js := rawJetStream(t, url)
	tuned := jetstream.StreamConfig{
		Name:        "ORDERS",
		Description: "tuned by operator",
		Subjects:    []string{"EXTRA.>"},
		MaxBytes:    1 << 20,
		Duplicates:  30 * time.Second,
		Storage:     jetstream.FileStorage,
	}
	if _, err := js.CreateStream(context.Background(), tuned); err != nil {
		t.Fatalf("create tuned stream: %v", err)
	}

	for i := 0; i < 2; i++ { // второй старт — идемпотентен
		if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), jsConfig("ORDERS")); err != nil {
			t.Fatalf("NewStreamBus #%d: %v", i, err)
		}
	}

	got := streamConfig(t, js, "ORDERS")
	if !slices.Contains(got.Subjects, "EXTRA.>") || !slices.Contains(got.Subjects, "ORDERS.>") || len(got.Subjects) != 2 {
		t.Fatalf("subjects = %v, want union {EXTRA.>, ORDERS.>}", got.Subjects)
	}
	if got.MaxBytes != tuned.MaxBytes || got.Duplicates != tuned.Duplicates || got.Description != tuned.Description || got.MaxAge != 0 {
		t.Fatalf("operator tuning clobbered: MaxBytes=%d Duplicates=%v Description=%q MaxAge=%v",
			got.MaxBytes, got.Duplicates, got.Description, got.MaxAge)
	}
}

func TestStreamBus_EnsureStreamsCreatesMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.Duplicates = 30 * time.Second
	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	got := streamConfig(t, rawJetStream(t, url), "ORDERS")
	if !slices.Equal(got.Subjects, []string{"ORDERS.>"}) || got.MaxAge != time.Hour || got.Duplicates != 30*time.Second {
		t.Fatalf("created stream config = %+v", got)
	}
}

// StreamManaged=false: кит не создаёт и не меняет стримы, но требует их наличия.
func TestStreamBus_UnmanagedStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	cfg := jsConfig("ORDERS")
	cfg.JetStream.StreamManaged = false

	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err == nil {
		t.Fatal("want error: unmanaged stream is missing")
	}

	js := rawJetStream(t, url)
	if _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{Name: "ORDERS", Subjects: []string{"EXTRA.>"}}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if _, err := natskit.NewStreamBus(connect(t, url), logkit.Nop(), cfg); err != nil {
		t.Fatalf("NewStreamBus with existing unmanaged stream: %v", err)
	}
	if got := streamConfig(t, js, "ORDERS"); !slices.Equal(got.Subjects, []string{"EXTRA.>"}) {
		t.Fatalf("unmanaged stream modified: subjects = %v", got.Subjects)
	}
}
