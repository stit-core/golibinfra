package natskit_test

import (
	"context"
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
