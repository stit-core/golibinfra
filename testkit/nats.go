package testkit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const NATSImage = "nats:2.11-alpine"

type NATSFixture struct {
	URL       string
	Container testcontainers.Container
}

type NATSOption func(*natsOptions)

type natsOptions struct {
	jetStream bool
}

// WithJetStream запускает сервер с `-js` — для тестов natskit.StreamBus и outboxkit.
func WithJetStream() NATSOption {
	return func(o *natsOptions) { o.jetStream = true }
}

func StartNATS(t *testing.T, opts ...NATSOption) *NATSFixture {
	t.Helper()
	var o natsOptions
	for _, opt := range opts {
		opt(&o)
	}
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        NATSImage,
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
	}
	if o.jetStream {
		req.Cmd = []string{"-js"}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("testkit: start nats: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Terminate(context.Background())
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("testkit: nats host: %v", err)
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		t.Fatalf("testkit: nats port: %v", err)
	}

	return &NATSFixture{
		URL:       fmt.Sprintf("nats://%s:%s", host, port.Port()),
		Container: c,
	}
}
