// Package otelkit — общий шов китов к глобальным OTel-провайдерам: одно имя
// instrumentation-scope и Must для конструкторов инструментов.
package otelkit

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const scope = "git.stit.tech/stit-core/golibinfra/"

func Meter(kit string) metric.Meter { return otel.Meter(scope + kit) }

func Tracer(kit string) trace.Tracer { return otel.Tracer(scope + kit) }

// Must — конструкторы OTel-инструментов ошибаются только на невалидном имени,
// то есть на баге в коде кита; здесь уместна паника на старте.
func Must[T any](inst T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("otelkit: instrument: %v", err))
	}
	return inst
}
