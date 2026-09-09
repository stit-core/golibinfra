package minio_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

// Глобальный delegate OTel привязывает package-level инструменты к первому
// SetMeterProvider, поэтому reader один на пакет, а тесты сравнивают дельты.
var metricsReader = sdkmetric.NewManualReader()

// MinIO поднимается один раз на пакет: контейнер на каждый тест стоит минуты.
var sharedMinIO *testkit.MinIOFixture

func TestMain(m *testing.M) {
	// Флаги тестов парсит m.Run, но контейнер поднимается раньше — testkit.RequireDocker
	// читает -short уже здесь.
	flag.Parse()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))

	tb := &packageTB{}
	sharedMinIO = tb.startMinIO()
	code := m.Run()
	tb.cleanup()
	os.Exit(code)
}

// minioFixture отдаёт общий контейнер; без Docker и под -short пропускает тест
// ровно так же, как это сделал бы testkit.StartMinIO.
func minioFixture(t *testing.T) *testkit.MinIOFixture {
	t.Helper()
	testkit.RequireDocker(t)
	if sharedMinIO == nil {
		t.Skip("testkit: MinIO fixture is unavailable")
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

type metricsSnapshot struct {
	ops       map[string]int64
	durations map[string]uint64
	buckets   map[string][]float64
}

func snapshot(t *testing.T) metricsSnapshot {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	s := metricsSnapshot{ops: map[string]int64{}, durations: map[string]uint64{}, buckets: map[string][]float64{}}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "storage_ops_total":
				for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
					op, _ := dp.Attributes.Value(attribute.Key("op"))
					res, _ := dp.Attributes.Value(attribute.Key("result"))
					s.ops[op.AsString()+"/"+res.AsString()] = dp.Value
				}
			case "storage_op_duration_seconds":
				for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
					op, _ := dp.Attributes.Value(attribute.Key("op"))
					s.durations[op.AsString()] = dp.Count
					s.buckets[op.AsString()] = dp.Bounds
				}
			}
		}
	}
	return s
}
