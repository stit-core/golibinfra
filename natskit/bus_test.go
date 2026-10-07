package natskit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestBus_QueueGroupLoadBalances(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	ctx := context.Background()
	subject := subj(t, "work")

	const n = 12
	got := make(chan struct{}, n*2)
	h := func(_ context.Context, _ []byte) error { got <- struct{}{}; return nil }
	for range 2 {
		if _, err := bus.SubscribeQueue(ctx, subject, "workers", h); err != nil {
			t.Fatalf("subscribe queue: %v", err)
		}
	}

	for range n {
		if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	count := 0
	deadline := time.After(5 * time.Second)
	for count < n {
		select {
		case <-got:
			count++
		case <-deadline:
			t.Fatalf("got %d/%d", count, n)
		}
	}
	// queue-group: каждое сообщение ровно одному подписчику → не больше n.
	expectSilence(t, got, 500*time.Millisecond, "duplicate delivery")
}

func TestBus_PublishSubscribe(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "roundtrip")

	got := make(chan []byte, 1)
	if _, err := bus.Subscribe(ctx, subject, func(_ context.Context, payload []byte) error {
		got <- payload
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Публикация — сырые байты: кит не навязывает JSON (для него есть PublishJSON).
	if err := bus.Publish(ctx, subject, []byte("raw bytes")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if payload := recv(t, got, 3*time.Second, "message"); string(payload) != "raw bytes" {
		t.Errorf("unexpected payload: %s", payload)
	}
}

func TestBus_PublishSubscribeJSON(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "json")

	type Msg struct {
		Value int `json:"value"`
	}

	got := make(chan Msg, 1)
	if _, err := natskit.SubscribeJSON(bus, ctx, subject, func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe json: %v", err)
	}

	if err := natskit.PublishJSON(bus, ctx, subject, Msg{Value: 42}); err != nil {
		t.Fatalf("publish json: %v", err)
	}

	if m := recv(t, got, 3*time.Second, "message"); m.Value != 42 {
		t.Errorf("expected value=42, got=%d", m.Value)
	}
}

func TestBus_SubscribeQueueJSON(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "queue-json")

	type Job struct {
		ID int `json:"id"`
	}
	got := make(chan Job, 4)
	if _, err := natskit.SubscribeQueueJSON(bus, ctx, subject, "workers", func(_ context.Context, j Job) error {
		got <- j
		return nil
	}); err != nil {
		t.Fatalf("subscribe queue json: %v", err)
	}

	if err := natskit.PublishJSON(bus, ctx, subject, Job{ID: 7}); err != nil {
		t.Fatalf("publish json: %v", err)
	}
	if j := recv(t, got, 3*time.Second, "job"); j.ID != 7 {
		t.Fatalf("job id = %d, want 7", j.ID)
	}
}

// Заголовки WithHeader доезжают до обработчика через HeadersFromContext —
// на них строится роутинг по event-type и версии схемы.
func TestBus_PublishHeadersReachHandler(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "headers")

	got := make(chan natsgo.Header, 1)
	if _, err := bus.Subscribe(ctx, subject, func(hctx context.Context, _ []byte) error {
		got <- natskit.HeadersFromContext(hctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	err := bus.Publish(ctx, subject, []byte(`{}`),
		natskit.WithHeader("Event-Type", "order.created"),
		natskit.WithHeader("Schema-Version", "2"),
		natskit.WithMsgID("evt-1"),
	)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	h := recv(t, got, 3*time.Second, "message headers")
	if got := h.Get("Event-Type"); got != "order.created" {
		t.Fatalf("Event-Type = %q", got)
	}
	if got := h.Get("Schema-Version"); got != "2" {
		t.Fatalf("Schema-Version = %q", got)
	}
	if got := h.Get("Nats-Msg-Id"); got != "evt-1" {
		t.Fatalf("Nats-Msg-Id = %q", got)
	}
}

// Ошибка обработчика логируется и не роняет подписку.
func TestBus_HandlerErrorLogged(t *testing.T) {
	log := &memLogger{}
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, log, natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "failing")

	called := make(chan struct{}, 2)
	if _, err := bus.Subscribe(ctx, subject, func(context.Context, []byte) error {
		called <- struct{}{}
		return errors.New("boom")
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	recv(t, called, 3*time.Second, "handler call")

	eventually(t, 3*time.Second, "handler error logged", func() bool {
		return log.has("error", "natskit: handler error")
	})
	if e := log.find("error", "natskit: handler error"); e == nil || !log.contains("instance") {
		t.Fatalf("handler error entry has no instance attribute: %v", log.dump())
	}
}

// Недекодируемый payload до обработчика не доходит (в JetStream такая ошибка
// терминальна — см. TestStreamBus_DecodeErrorGoesToDLQ).
func TestSubscribeJSON_SkipsUndecodablePayload(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx := context.Background()
	subject := subj(t, "broken")

	type Msg struct {
		Value int `json:"value"`
	}
	called := make(chan struct{}, 1)
	if _, err := natskit.SubscribeJSON(bus, ctx, subject, func(context.Context, Msg) error {
		called <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("subscribe json: %v", err)
	}
	if err := bus.Publish(ctx, subject, []byte(`not json`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expectSilence(t, called, 500*time.Millisecond, "handler call on undecodable payload")
}

func TestBus_SubscribeRejectsCancelledContext(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bus.Subscribe(ctx, subj(t, "x"), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("want error for cancelled ctx")
	}
}

// Ноль потолка обработчиков — не дефолт кита, а ErrConfig: семафор без веса не пропустил бы
// ни одного сообщения.
func TestNewBus_RejectsZeroMaxInFlight(t *testing.T) {
	if _, err := natskit.NewBus(natskit.Config{}, nil, logkit.Nop()); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewBus(zero MaxInFlight) err = %v, want ErrConfig", err)
	}
}

// Subject и фильтр, которые сервер отбросил бы молча (публикация с пустым токеном или wildcard'ом,
// подписка с `>` не последним), — ошибка nats.ErrBadSubject сразу, по правилу, которым проверяет и
// фейк natstest.
func TestBus_RejectsSubjectsTheServerDrops(t *testing.T) {
	conn := connect(t, natsURL(t))
	bus := newBus(t, conn, logkit.Nop(), natskit.Config{})
	stream := newStreamBus(t, jsConfig("BADSUBJ"))
	ctx := context.Background()
	h := func(context.Context, []byte) error { return nil }
	for name, err := range map[string]error{
		"publish empty token":       bus.Publish(ctx, "orders..created", []byte(`{}`)),
		"publish wildcard":          bus.Publish(ctx, "orders.*", []byte(`{}`)),
		"stream publish wildcard":   stream.PublishStream(ctx, "BADSUBJ.>", []byte(`{}`)),
		"subscribe > not last":      func() error { _, err := bus.Subscribe(ctx, "orders.>.x", h); return err }(),
		"stream filter empty token": func() error { _, err := stream.SubscribeStream(ctx, "BADSUBJ", "BADSUBJ..x", "d", h); return err }(),
	} {
		if !errors.Is(err, natsgo.ErrBadSubject) {
			t.Errorf("%s: err = %v, want nats.ErrBadSubject", name, err)
		}
	}
	results, err := stream.PublishStreamBatch(ctx, []natskit.StreamMsg{{Subject: "BADSUBJ..x"}, {Subject: "BADSUBJ.ok", Payload: []byte(`{}`)}})
	if err != nil || !errors.Is(results[0], natsgo.ErrBadSubject) || results[1] != nil {
		t.Errorf("batch: results = %v, err = %v; want ErrBadSubject only for the bad subject", results, err)
	}
}
