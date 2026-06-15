package natskit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	natsgo "github.com/nats-io/nats.go"

	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/natskit"
)

func startJetStreamNATS(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "nats:latest",
		Cmd:          []string{"-js"},
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start jetstream nats: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return "nats://" + host + ":" + port.Port()
}

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
}

func TestStreamBus_WildcardSubjectInContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn := natskit.ExportConn(nc)

	cfg := natskit.Config{
		JetStream: natskit.JetStreamConfig{
			Enabled:     true,
			StreamNames: []string{"ORDERS"},
			Retention:   "limits",
			MaxAge:      time.Hour,
			MaxInFlight: 32,
			AckWait:     5 * time.Second,
		},
	}
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	gotSubject := make(chan string, 1)
	if err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.>", "orders-wild", func(ctx context.Context, _ []byte) error {
		gotSubject <- natskit.SubjectFromContext(ctx)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := sb.PublishStream(context.Background(), "ORDERS.created", []byte("{}")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case s := <-gotSubject:
		if s != "ORDERS.created" {
			t.Fatalf("SubjectFromContext = %q, want ORDERS.created", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for jetstream message")
	}
}

func TestStreamBus_PublishSubscribe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn := natskit.ExportConn(nc)

	cfg := natskit.Config{
		JetStream: natskit.JetStreamConfig{
			Enabled:     true,
			StreamNames: []string{"ORDERS"},
			Retention:   "limits",
			MaxAge:      time.Hour,
			MaxInFlight: 32,
			AckWait:     5 * time.Second,
		},
	}
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	type Msg struct {
		ID int `json:"id"`
	}
	got := make(chan Msg, 1)
	if err := natskit.SubscribeStreamJSON(sb, context.Background(), "ORDERS", "ORDERS.created", "orders-worker", func(_ context.Context, m Msg) error {
		got <- m
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := natskit.PublishStreamJSON(sb, context.Background(), "ORDERS.created", Msg{ID: 7}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case m := <-got:
		if m.ID != 7 {
			t.Fatalf("want id=7, got %d", m.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for jetstream message")
	}
}

func TestStreamBus_PublishBatchDedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn := natskit.ExportConn(nc)

	cfg := natskit.Config{
		JetStream: natskit.JetStreamConfig{
			Enabled:     true,
			StreamNames: []string{"ORDERS"},
			Retention:   "limits",
			MaxAge:      time.Hour,
			Duplicates:  time.Minute,
			MaxInFlight: 32,
			AckWait:     5 * time.Second,
		},
	}
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	type order struct {
		ID int `json:"id"`
	}
	got := make(chan int, 8)
	if err := natskit.SubscribeStreamJSON(sb, context.Background(), "ORDERS", "ORDERS.created", "orders-batch",
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
	select {
	case extra := <-got:
		t.Fatalf("лишняя доставка id=%d — дедуп не сработал: seen=%v", extra, seen)
	case <-time.After(1 * time.Second):
	}
	if seen[1] != 1 || seen[2] != 1 {
		t.Fatalf("seen = %v, want {1:1, 2:1}", seen)
	}
}

func TestStreamBus_PublishBatchPartialError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	url := startJetStreamNATS(t)
	nc, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn := natskit.ExportConn(nc)

	cfg := natskit.Config{
		JetStream: natskit.JetStreamConfig{
			Enabled:     true,
			StreamNames: []string{"ORDERS"},
			Retention:   "limits",
			MaxAge:      time.Hour,
			MaxInFlight: 32,
			AckWait:     5 * time.Second,
		},
	}
	sb, err := natskit.NewStreamBus(conn, logkit.Nop(), cfg)
	if err != nil {
		t.Fatalf("NewStreamBus: %v", err)
	}

	delivered := make(chan struct{}, 8)
	if err := sb.SubscribeStream(context.Background(), "ORDERS", "ORDERS.created", "orders-partial",
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
		select {
		case <-delivered:
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for valid deliveries")
		}
	}
	select {
	case <-delivered:
		t.Fatal("лишняя доставка — oversized всё-таки опубликовалось")
	case <-time.After(1 * time.Second):
	}
}
