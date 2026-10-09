package testkit

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"git.stit.tech/stit-core/golibinfra/pgkit"
)

// Option — функциональная опция контейнерных хелперов. Опция, не применимая к
// вызванному Start*, обрывает тест через t.Fatalf, а не игнорируется молча.
type Option func(*options)

type options struct {
	image     string
	sources   []pgkit.Source
	jetStream bool
}

// WithImage подменяет образ контейнера — расширения (pgvector, postgis) и другие версии.
func WithImage(ref string) Option {
	return func(o *options) { o.image = ref }
}

// WithMigrations применяет источники (embed.FS китов, каталоги приложения) сразу
// после старта Postgres — через pgkit.MigrateUpAll, как MigrateModule.
func WithMigrations(sources ...pgkit.Source) Option {
	return func(o *options) { o.sources = append(o.sources, sources...) }
}

// WithMigrationsDir — каталог миграций приложения (источник pgkit.AppSourceName).
func WithMigrationsDir(path string) Option {
	return WithMigrations(pgkit.DirSource(pgkit.AppSourceName, path))
}

// WithJetStream запускает NATS с `-js` — для тестов natskit.StreamBus и outboxkit.
func WithJetStream() Option {
	return func(o *options) { o.jetStream = true }
}

// support описывает, какие опции осмысленны для конкретного Start*.
type support struct {
	helper     string
	migrations bool
	jetStream  bool
}

func newOptions(t testing.TB, sup support, defaultImage string, opts []Option) options {
	t.Helper()
	o := options{image: defaultImage}
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.sources) > 0 && !sup.migrations {
		t.Fatalf("testkit: %s does not accept WithMigrations/WithMigrationsDir", sup.helper)
	}
	if o.jetStream && !sup.jetStream {
		t.Fatalf("testkit: %s does not accept WithJetStream", sup.helper)
	}
	return o
}

const dockerHealthTimeout = 10 * time.Second

// dockerHealth проверяется один раз на процесс: доступность демона в пределах
// одного прогона не меняется, а зонд открывает docker-клиент.
var dockerHealth = sync.OnceValue(func() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("provider panicked: %v", r)
		}
	}()

	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerHealthTimeout)
	defer cancel()
	return provider.Health(ctx)
})

// RequireDocker пропускает тест под -short, а без доступного Docker обрывает его
// (t.Fatalf): integration-тест не зеленеет, не открыв контейнер. Все Start*
// вызывают его сами; напрямую он нужен тестам со своим ContainerRequest.
func RequireDocker(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("testkit: -short runs without Docker")
	}
	if err := dockerHealth(); err != nil {
		t.Fatalf("testkit: Docker is unavailable (go test -short skips integration tests): %v", err)
	}
}

// testLogger — лог testcontainers в t.Log, пока тест идёт. Жнец контейнеров (ryuk) один на
// процесс и держит логгер первой фикстуры: под -count>1 он писал бы в t завершённого теста, а
// такая запись — panic «Log in goroutine after … has completed».
type testLogger struct {
	mu   sync.RWMutex
	t    testing.TB
	done bool
}

func newTestLogger(t testing.TB) *testLogger {
	l := &testLogger{t: t}
	t.Cleanup(func() {
		l.mu.Lock()
		l.done = true
		l.mu.Unlock()
	})
	return l
}

// Printf — метод логгера testcontainers.
func (l *testLogger) Printf(format string, v ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.done {
		l.t.Helper()
		l.t.Logf(format, v...)
	}
}
