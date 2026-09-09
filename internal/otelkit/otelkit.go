// Package otelkit — общий шов китов к глобальным OTel-провайдерам: одно имя
// instrumentation-scope с версией модуля, общие бакеты длительностей и Must для
// конструкторов инструментов.
package otelkit

import (
	"fmt"
	"runtime/debug"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ModulePath — module path golibinfra: префикс instrumentation-scope всех китов.
const ModulePath = "git.stit.tech/stit-core/golibinfra"

// DevVersion — версия инструментации в сборке, которая не несёт версии модуля
// (тесты самого модуля, go run, replace на локальный каталог).
const DevVersion = "dev"

// Scope возвращает имя instrumentation-scope пакета: `<ModulePath>/<pkg>`.
// Значение попадает в otel_scope_name у каждой метрики и спана кита.
func Scope(pkg string) string { return ModulePath + "/" + pkg }

// Version возвращает версию golibinfra в текущей сборке — она уходит в
// otel_scope_version. DevVersion, если версия недоступна.
func Version() string { return version }

// Meter возвращает Meter глобального провайдера со scope и версией пакета.
func Meter(pkg string) metric.Meter {
	return otel.Meter(Scope(pkg), metric.WithInstrumentationVersion(version))
}

// Tracer возвращает Tracer глобального провайдера со scope и версией пакета.
func Tracer(pkg string) trace.Tracer {
	return otel.Tracer(Scope(pkg), trace.WithInstrumentationVersion(version))
}

// DurationBuckets — общие границы гистограмм длительности (секунды) для всех
// китов: одна форма `_seconds`-метрик на дашбордах и в алертах. Применяется
// вместе с metric.WithUnit("s").
func DurationBuckets() metric.HistogramOption {
	return metric.WithExplicitBucketBoundaries(durationBounds...)
}

// Must — конструкторы OTel-инструментов ошибаются только на невалидном имени,
// то есть на баге в коде кита; здесь уместна паника на старте.
func Must[T any](inst T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("otelkit: instrument: %v", err))
	}
	return inst
}

var durationBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

var version = readVersion(debug.ReadBuildInfo)

func readVersion(read func() (*debug.BuildInfo, bool)) string {
	bi, ok := read()
	if !ok {
		return DevVersion
	}
	if bi.Main.Path == ModulePath {
		return normalizeVersion(bi.Main.Version)
	}
	for _, d := range bi.Deps {
		if d.Path != ModulePath {
			continue
		}
		// Replace-директива подменяет версию: у локального каталога её нет.
		if d.Replace != nil {
			return normalizeVersion(d.Replace.Version)
		}
		return normalizeVersion(d.Version)
	}
	return DevVersion
}

func normalizeVersion(v string) string {
	if v == "" || !strings.HasPrefix(v, "v") {
		return DevVersion
	}
	return v
}
