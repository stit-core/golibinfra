package pgstore_test

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Глобальный delegate OTel привязывает package-level инструменты к первому
// SetMeterProvider, поэтому reader один на пакет, а тест сравнивает дельты.
var metricsReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricsReader)))
	os.Exit(m.Run())
}
