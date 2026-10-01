package minio_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

// MinIO поднимается один раз на пакет: контейнер на каждый тест стоит минуты.
var sharedMinIO *testkit.MinIOFixture

func TestMain(m *testing.M) {
	// Флаги тестов парсит m.Run, но контейнер поднимается раньше — testkit.RequireDocker
	// читает -short уже здесь.
	flag.Parse()

	tb := &packageTB{}
	sharedMinIO = tb.startMinIO()
	code := m.Run()
	tb.cleanup()
	os.Exit(code)
}

// minioFixture отдаёт общий контейнер; под -short пропускает тест, без Docker
// роняет — ровно так же, как это сделал бы testkit.StartMinIO.
func minioFixture(t *testing.T) *testkit.MinIOFixture {
	t.Helper()
	testkit.RequireDocker(t)
	if sharedMinIO == nil {
		t.Fatal("testkit: MinIO fixture is unavailable")
	}
	return sharedMinIO
}

// packageTB — testing.TB для контейнерных хелперов вне теста: только он
// позволяет держать один контейнер на пакет через TestMain.
type packageTB struct {
	testing.TB

	mu       sync.Mutex
	cleanups []func()
}

type skipRun struct{}

func (tb *packageTB) startMinIO() (fix *testkit.MinIOFixture) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(skipRun); !ok {
				panic(r)
			}
			fix = nil
		}
	}()
	return testkit.StartMinIO(tb)
}

func (tb *packageTB) cleanup() {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for _, v := range slices.Backward(tb.cleanups) {
		v()
	}
	tb.cleanups = nil
}

func (tb *packageTB) Helper() {}

func (tb *packageTB) Name() string { return "TestMain" }

func (tb *packageTB) Failed() bool { return false }

func (tb *packageTB) Context() context.Context { return context.Background() }

func (tb *packageTB) Cleanup(f func()) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.cleanups = append(tb.cleanups, f)
}

func (tb *packageTB) Log(args ...any) { fmt.Fprintln(os.Stderr, args...) }

func (tb *packageTB) Logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

func (tb *packageTB) Error(args ...any) { tb.Log(args...) }

func (tb *packageTB) Errorf(format string, args ...any) { tb.Logf(format, args...) }

func (tb *packageTB) Fail() {}

func (tb *packageTB) FailNow() {}

func (tb *packageTB) Fatal(args ...any) { panic(fmt.Sprint(args...)) }

func (tb *packageTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

func (tb *packageTB) Skip(args ...any) { tb.Log(args...); panic(skipRun{}) }

func (tb *packageTB) Skipf(format string, args ...any) { tb.Logf(format, args...); panic(skipRun{}) }

func (tb *packageTB) SkipNow() { panic(skipRun{}) }

func (tb *packageTB) Skipped() bool { return false }
