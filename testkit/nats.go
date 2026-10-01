package testkit

import (
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NATSImage — образ StartNATS по умолчанию; переопределяется WithImage.
const NATSImage = "nats:2.14.6-alpine"

const startupTimeout = 60 * time.Second

// NATSFixture — живой NATS теста: клиентский URL и handle контейнера.
type NATSFixture struct {
	URL       string
	Container testcontainers.Container
}

// StartNATS поднимает NATS (с `-js` при WithJetStream).
// Под -short пропускает тест, без Docker обрывает (RequireDocker); контейнер
// снимается в t.Cleanup.
func StartNATS(t testing.TB, opts ...Option) *NATSFixture {
	t.Helper()
	RequireDocker(t)
	o := newOptions(t, support{helper: "StartNATS", jetStream: true}, NATSImage, opts)

	req := testcontainers.ContainerRequest{
		Image:        o.image,
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor: wait.ForAll(
			wait.ForLog("Server is ready"),
			wait.ForListeningPort("4222/tcp"),
		).WithStartupTimeoutDefault(startupTimeout),
	}
	if o.jetStream {
		req.Cmd = []string{"-js"}
	}
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
		Logger:           tclog.TestLogger(t),
	})
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("testkit: start nats: %v", err)
	}

	return &NATSFixture{URL: endpointURL(t, c, "nats", "4222"), Container: c}
}

// endpointURL — <scheme>://<host>:<mapped port> контейнера.
func endpointURL(t testing.TB, c testcontainers.Container, scheme, port string) string {
	t.Helper()
	ctx := t.Context()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("testkit: container host: %v", err)
	}
	mapped, err := c.MappedPort(ctx, port)
	if err != nil {
		t.Fatalf("testkit: container port %s: %v", port, err)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, mapped.Port())
}
