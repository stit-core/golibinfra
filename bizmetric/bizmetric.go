// Package bizmetric — конвенциональный helper для бизнес-метрик: имена
// форсятся в форму `biz_<domain>_<name>_<suffix>` (Counter → `_total`,
// Histogram → `_seconds`, Gauge — без суффикса), нарушение конвенции — panic
// на старте. Инструменты создаются через переданный metric.Meter.
package bizmetric

import (
	"fmt"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/metric"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Counter создаёт Int64Counter с именем `biz_<domain>_<name>_total`.
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

// Gauge создаёт Int64UpDownCounter (gauge для значений, которые пишет само
// приложение) с именем `biz_<domain>_<name>`. Для observable-источников —
// meter.Int64ObservableGauge с именем из MustName.
func Gauge(meter metric.Meter, domain, name string, opts ...metric.Int64UpDownCounterOption) metric.Int64UpDownCounter {
	full := mustName(domain, name)
	g, err := meter.Int64UpDownCounter(full, opts...)
	if err != nil {
		panic(fmt.Sprintf("bizmetric: create gauge %s: %v", full, err))
	}
	return g
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
	switch {
	case !nameRE.MatchString(s):
		panic(fmt.Sprintf("bizmetric: invalid %s %q (need ^[a-z][a-z0-9_]*$)", what, s))
	case strings.HasPrefix(s, "biz_"):
		panic(fmt.Sprintf("bizmetric: invalid %s %q: prefix biz_ is added by the helper", what, s))
	case strings.HasSuffix(s, "_total") || strings.HasSuffix(s, "_seconds"):
		panic(fmt.Sprintf("bizmetric: invalid %s %q: suffixes _total/_seconds are added by the helper", what, s))
	}
}
