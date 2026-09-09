// Package bizmetric — конвенциональный helper для бизнес-метрик: имена форсятся
// в форму `biz_<domain>_<name>_<suffix>`, нарушение конвенции — panic на старте.
// Инструменты создаются через переданный metric.Meter.
package bizmetric

import (
	"fmt"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
)

// UnitEvent — unit счётчиков событий. Аннотация в фигурных скобках: OTLP-native
// бэкенды показывают её как есть, Prometheus-трансляция отбрасывает.
const UnitEvent = "{event}"

// UnitItem — unit gauge-инструментов: «сколько сейчас штук».
const UnitItem = "{item}"

// UnitSeconds — unit гистограмм длительности.
const UnitSeconds = "s"

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedSuffixes подставляет либо сам helper, либо Prometheus-трансляция
// (`_count`/`_sum`/`_bucket` у гистограмм, `_created` у счётчиков): в domain или
// name они дают двойной суффикс и коллизию с чужой серией.
var reservedSuffixes = []string{"_total", "_seconds", "_count", "_sum", "_bucket", "_created"}

// Counter создаёт Int64Counter с именем `biz_<domain>_<name>_total`.
func Counter(meter metric.Meter, domain, name string, opts ...metric.Int64CounterOption) metric.Int64Counter {
	full := mustName(domain, name) + "_total"
	c, err := meter.Int64Counter(full, append([]metric.Int64CounterOption{metric.WithUnit(UnitEvent)}, opts...)...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create counter %s: %v", full, err))
	}
	return c
}

// Histogram создаёт Float64Histogram с именем `biz_<domain>_<name>_seconds`,
// unit `s` и общими для репо бакетами длительности.
func Histogram(meter metric.Meter, domain, name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram {
	full := mustName(domain, name) + "_seconds"
	defaults := []metric.Float64HistogramOption{metric.WithUnit(UnitSeconds), otelkit.DurationBuckets()}
	h, err := meter.Float64Histogram(full, append(defaults, opts...)...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create histogram %s: %v", full, err))
	}
	return h
}

// Gauge создаёт Int64Gauge с именем `biz_<domain>_<name>`: Record пишет текущее
// значение. Для observable-источников — meter.Int64ObservableGauge с именем из
// MustName.
func Gauge(meter metric.Meter, domain, name string, opts ...metric.Int64GaugeOption) metric.Int64Gauge {
	full := mustName(domain, name)
	g, err := meter.Int64Gauge(full, append([]metric.Int64GaugeOption{metric.WithUnit(UnitItem)}, opts...)...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create gauge %s: %v", full, err))
	}
	return g
}

// UpDownCounter создаёт Int64UpDownCounter с именем `biz_<domain>_<name>`:
// Add пишет дельту к текущему значению.
func UpDownCounter(meter metric.Meter, domain, name string, opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter {
	full := mustName(domain, name)
	c, err := meter.Int64UpDownCounter(full, append([]metric.Int64UpDownCounterOption{metric.WithUnit(UnitItem)}, opts...)...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create updowncounter %s: %v", full, err))
	}
	return c
}

// MustName собирает имя `biz_<domain>_<name>` с валидацией — для инструментов,
// которые регистрируются вручную (ObservableGauge и т.п.).
func MustName(domain, name string) string {
	return mustName(domain, name)
}

func mustName(domain, name string) string {
	validate("domain", domain)
	validate("name", name)
	return "biz_" + domain + "_" + name
}

func validate(what, s string) {
	if !nameRE.MatchString(s) {
		panic(fmt.Sprintf("bizmetric: invalid %s %q (need ^[a-z][a-z0-9_]*$)", what, s))
	}
	if strings.HasPrefix(s, "biz_") {
		panic(fmt.Sprintf("bizmetric: invalid %s %q: prefix biz_ is added by the helper", what, s))
	}
	for _, suffix := range reservedSuffixes {
		if strings.HasSuffix(s, suffix) {
			panic(fmt.Sprintf("bizmetric: invalid %s %q: suffix %s is reserved (added by the helper or by Prometheus translation)", what, s, suffix))
		}
	}
}
