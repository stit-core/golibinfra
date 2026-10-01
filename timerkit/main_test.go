package timerkit

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Провайдер трейсов глобален и ставится до сборки runner'ов, поэтому recorder один
// на пакет, а тесты фильтруют спаны по имени job.
var spanRecorder = tracetest.NewSpanRecorder()

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder)))
	os.Exit(m.Run())
}
