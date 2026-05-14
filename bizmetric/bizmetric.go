// Package bizmetric — конвенциональный helper для бизнес-метрик.
//
// Все имена форсятся в форму `biz_<domain>_<name>_<suffix>`:
//   - Counter → суффикс `_total`
//   - Histogram (seconds) → суффикс `_seconds`
//   - Gauge → без суффикса
//
// Resource-attrs (service.name, tenant, environment) добавляются OTel
// SDK автоматически — повторно их в labels класть не нужно.
//
// Префикс `biz_` нужен чтобы дашборд "Apps/business" мог использовать
// автодискавер `{__name__=~"biz_.*"}` без ручного списка метрик.
package bizmetric

import (
	"fmt"
	"regexp"

	"go.opentelemetry.io/otel/metric"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Counter создаёт Int64Counter с именем `biz_<domain>_<name>_total`.
// Panic-ует на невалидные domain/name (snake_case ASCII, без префикса/суффикса).
func Counter(meter metric.Meter, domain, name string, opts ...metric.Int64CounterOption) metric.Int64Counter {
	full := mustName(domain, name) + "_total"
	c, err := meter.Int64Counter(full, opts...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create counter %s: %v", full, err))
	}
	return c
}

// Histogram создаёт Float64Histogram с именем `biz_<domain>_<name>_seconds`.
func Histogram(meter metric.Meter, domain, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram {
	full := mustName(domain, name) + "_seconds"
	h, err := meter.Float64Histogram(full, opts...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create histogram %s: %v", full, err))
	}
	return h
}

// Gauge создаёт Int64UpDownCounter (наш способ выразить gauge для
// non-observable значений) с именем `biz_<domain>_<name>`.
// Для observable-источников используйте meter.Int64ObservableGauge напрямую
// и валидируйте имя через MustName.
func Gauge(meter metric.Meter, domain, name string, opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter {
	full := mustName(domain, name)
	g, err := meter.Int64UpDownCounter(full, opts...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create gauge %s: %v", full, err))
	}
	return g
}

// MustName собирает имя `biz_<domain>_<name>` с валидацией.
// Полезен если нужно вручную зарегистрировать ObservableGauge/Counter.
func MustName(domain, name string) string {
	return mustName(domain, name)
}

func mustName(domain, name string) string {
	if !nameRE.MatchString(domain) {
		panic(fmt.Sprintf("bizmetric: invalid domain %q (need ^[a-z][a-z0-9_]*$)", domain))
	}
	if !nameRE.MatchString(name) {
		panic(fmt.Sprintf("bizmetric: invalid name %q (need ^[a-z][a-z0-9_]*$)", name))
	}
	return "biz_" + domain + "_" + name
}
