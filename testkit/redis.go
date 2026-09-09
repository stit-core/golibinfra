package testkit

import (
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tclog "github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RedisImage — образ StartRedis по умолчанию; переопределяется WithImage.
const RedisImage = "redis:7-alpine"

// RedisFixture — живой Redis теста: клиентский URL и handle контейнера.
type RedisFixture struct {
	URL       string
	Container testcontainers.Container
}

// StartRedis поднимает Redis.
// Пропускает тест под -short и без Docker; контейнер снимается в t.Cleanup.
func StartRedis(t testing.TB, opts ...Option) *RedisFixture {
	t.Helper()
	RequireDocker(t)
	o := newOptions(t, support{helper: "StartRedis"}, RedisImage, opts)

	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        o.image,
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForLog("Ready to accept connections"),
				wait.ForListeningPort("6379/tcp"),
			).WithStartupTimeoutDefault(startupTimeout),
		},
		Started: true,
		Logger:  tclog.TestLogger(t),
	})
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("testkit: start redis: %v", err)
	}

	return &RedisFixture{URL: endpointURL(t, c, "redis", "6379"), Container: c}
}
