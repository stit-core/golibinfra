package testkit

import (
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MinIOImage — образ StartMinIO по умолчанию; переопределяется WithImage.
const MinIOImage = "minio/minio:RELEASE.2025-01-20T14-49-07Z"

// MinIOFixture — живой MinIO теста: S3-эндпоинт (host:port, plaintext — storagekit.Config
// берёт "http://"+Endpoint), креды и handle контейнера. Бакеты создаёт тест — фикстура их не заводит.
type MinIOFixture struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Container *tcminio.MinioContainer
}

// StartMinIO поднимает MinIO (S3-совместимое хранилище для storagekit) и ждёт, пока сервер
// готов к записи. Под -short пропускает тест, без Docker обрывает (RequireDocker); контейнер
// снимается в t.Cleanup.
func StartMinIO(t testing.TB, opts ...Option) *MinIOFixture {
	t.Helper()
	RequireDocker(t)
	o := newOptions(t, support{helper: "StartMinIO"}, MinIOImage, opts)

	c, err := tcminio.Run(t.Context(), o.image, testcontainers.WithLogger(newTestLogger(t)),
		// /minio/health/live отвечает 200 раньше, чем поднят слой объектов: под нагрузкой первый
		// запрос теста получал «Server not initialized yet». /minio/health/cluster ждёт его.
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/cluster").WithPort("9000/tcp")))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("testkit: start minio: %v", err)
	}

	endpoint, err := c.ConnectionString(t.Context())
	if err != nil {
		t.Fatalf("testkit: minio endpoint: %v", err)
	}

	return &MinIOFixture{
		Endpoint:  endpoint,
		AccessKey: c.Username,
		SecretKey: c.Password,
		Container: c,
	}
}
