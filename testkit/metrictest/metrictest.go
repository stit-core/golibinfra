// Package metrictest — проверка метрик в тестах: провайдер теста для кода, которому
// передают Meter, провайдер процесса (глобальный MeterProvider) для китов, снимки и
// дельты серий, коллизии имён после трансляции в Prometheus.
package metrictest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Kind — тип инструмента серии.
type Kind string

// Типы инструментов: Counter — монотонная сумма, UpDownCounter — немонотонная.
const (
	Counter       Kind = "counter"
	UpDownCounter Kind = "updowncounter"
	Gauge         Kind = "gauge"
	Histogram     Kind = "histogram"
)

// Series — одна серия метрики: инструмент (scope, имя, тип, unit) и набор labels.
// Value — сумма у Counter и UpDownCounter, значение у Gauge, сумма наблюдений у
// Histogram; Count и Bounds — только у Histogram.
type Series struct {
	Scope  string
	Name   string
	Kind   Kind
	Unit   string
	Labels map[string]string
	Value  float64
	Count  uint64
	Bounds []float64
}

// String — «name{k=v,…} = value» для сообщений тестов.
func (s Series) String() string {
	keys := slices.Sorted(maps.Keys(s.Labels))
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + s.Labels[k]
	}
	v := fmt.Sprintf("%g", s.Value)
	if s.Kind == Histogram {
		v = fmt.Sprintf("count %d, sum %g", s.Count, s.Value)
	}
	return s.Name + "{" + strings.Join(pairs, ",") + "} = " + v
}

func (s Series) key() string {
	keys := slices.Sorted(maps.Keys(s.Labels))
	var b strings.Builder
	b.WriteString(s.Scope + "\x00" + s.Name + "\x00" + string(s.Kind) + "\x00" + s.Unit)
	for _, k := range keys {
		b.WriteString("\x00" + k + "=" + s.Labels[k])
	}
	return b.String()
}

// Snapshot — серии провайдера на момент Collect или их разница (Sub).
type Snapshot struct {
	series []Series
}

// Provider — MeterProvider с ManualReader, чьи серии снимает Collect. Свой у теста
// (NewProvider): код под тестом получает его Meter или сам провайдер, записи других
// тестов и прошлых прогонов (-count) в снимок не попадают.
type Provider struct {
	*sdkmetric.MeterProvider
	reader *sdkmetric.ManualReader
}

// NewProvider — провайдер теста; глобальный MeterProvider OTel он не трогает.
func NewProvider() *Provider {
	reader := sdkmetric.NewManualReader()
	return &Provider{MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), reader: reader}
}

var (
	process = NewProvider()
	install sync.Once
)

// Collect снимает серии провайдера процесса — глобального MeterProvider, в который
// пишут киты. Его ставит первый Collect, а не импорт пакета: инструменты, созданные
// раньше от глобального провайдера OTel, делегат переносит на него, записи до первого
// Collect не видны — тест снимает before до действия. Подмена глобального провайдера
// после первого Collect (telemetry.Init, otel.SetMeterProvider) роняет Collect: в
// таком бинаре метрики проверяют через NewProvider. Снимок видит записи всего
// процесса: тест, сравнивающий дельты, не идёт параллельно с тестами тех же серий.
func Collect(t testing.TB) Snapshot {
	t.Helper()
	install.Do(func() { otel.SetMeterProvider(process.MeterProvider) })
	if otel.GetMeterProvider() != process.MeterProvider {
		t.Fatalf("metrictest: глобальный MeterProvider подменён после первого Collect (telemetry.Init, otel.SetMeterProvider) — " +
			"провайдер процесса один; Meter, переданный коду, проверяет NewProvider")
	}
	return process.Collect(t)
}

// Collect снимает серии провайдера.
func (p *Provider) Collect(t testing.TB) Snapshot {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := p.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("metrictest: collect: %v", err)
	}
	var out []Series
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out = appendSeries(out, sm.Scope.Name, m)
		}
	}
	return Snapshot{series: out}
}

func appendSeries(out []Series, scope string, m metricdata.Metrics) []Series {
	base := Series{Scope: scope, Name: m.Name, Unit: m.Unit}
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		return appendSums(out, base, d.IsMonotonic, d.DataPoints)
	case metricdata.Sum[float64]:
		return appendSums(out, base, d.IsMonotonic, d.DataPoints)
	case metricdata.Gauge[int64]:
		return appendGauges(out, base, d.DataPoints)
	case metricdata.Gauge[float64]:
		return appendGauges(out, base, d.DataPoints)
	case metricdata.Histogram[int64]:
		return appendHistograms(out, base, d.DataPoints)
	case metricdata.Histogram[float64]:
		return appendHistograms(out, base, d.DataPoints)
	}
	return out
}

func appendSums[N int64 | float64](out []Series, base Series, monotonic bool, points []metricdata.DataPoint[N]) []Series {
	base.Kind = UpDownCounter
	if monotonic {
		base.Kind = Counter
	}
	for _, p := range points {
		s := base
		s.Labels = labelMap(p.Attributes.ToSlice())
		s.Value = float64(p.Value)
		out = append(out, s)
	}
	return out
}

func appendGauges[N int64 | float64](out []Series, base Series, points []metricdata.DataPoint[N]) []Series {
	base.Kind = Gauge
	for _, p := range points {
		s := base
		s.Labels = labelMap(p.Attributes.ToSlice())
		s.Value = float64(p.Value)
		out = append(out, s)
	}
	return out
}

func appendHistograms[N int64 | float64](out []Series, base Series, points []metricdata.HistogramDataPoint[N]) []Series {
	base.Kind = Histogram
	for _, p := range points {
		s := base
		s.Labels = labelMap(p.Attributes.ToSlice())
		s.Value = float64(p.Sum)
		s.Count = p.Count
		s.Bounds = slices.Clone(p.Bounds)
		out = append(out, s)
	}
	return out
}

// Sub — разница с более ранним снимком: у Counter, UpDownCounter и Histogram —
// приращение, у Gauge — значение этого снимка. Серия, которой не было в before,
// остаётся и с нулём (UpDownCounter +1 −1); серия before без изменений отброшена.
func (s Snapshot) Sub(before Snapshot) Snapshot {
	prev := make(map[string]Series, len(before.series))
	for _, b := range before.series {
		prev[b.key()] = b
	}
	var out []Series
	for _, a := range s.series {
		if b, ok := prev[a.key()]; ok && a.Kind != Gauge {
			if a.Value == b.Value && a.Count == b.Count {
				continue
			}
			a.Value -= b.Value
			a.Count -= b.Count
		}
		out = append(out, a)
	}
	return Snapshot{series: out}
}

// Get — серия name с ровно этими labels (пары ключ-значение); без неё тест падает
// со списком серий метрики.
func (s Snapshot) Get(t testing.TB, name string, labels ...string) Series {
	t.Helper()
	want := pairs(labels)
	for _, x := range s.series {
		if x.Name == name && maps.Equal(x.Labels, want) {
			return x
		}
	}
	t.Fatalf("metrictest: нет серии %s с labels %v; серии метрики: %v", name, want, s.Find(name))
	return Series{}
}

// Find — серии name, у которых есть эти labels (пары ключ-значение); прочие labels
// серии не важны.
func (s Snapshot) Find(name string, labels ...string) []Series {
	want := pairs(labels)
	var out []Series
	for _, x := range s.series {
		if x.Name == name && contains(x.Labels, want) {
			out = append(out, x)
		}
	}
	return out
}

// All — все серии снимка.
func (s Snapshot) All() []Series {
	return slices.Clone(s.series)
}

// Value — сумма Value серий Find(name, labels...); 0, если серий нет.
func (s Snapshot) Value(name string, labels ...string) float64 {
	var sum float64
	for _, x := range s.Find(name, labels...) {
		sum += x.Value
	}
	return sum
}

// Collisions — имена Prometheus (PromName), в которые переходят разные инструменты
// снимка: в Prometheus их серии сливаются в одну метрику. Ключ — имя Prometheus,
// значение — инструменты «scope name».
func (s Snapshot) Collisions() map[string][]string {
	by := map[string]map[string]bool{}
	for _, x := range s.series {
		n := PromName(x.Name, x.Unit, x.Kind)
		if by[n] == nil {
			by[n] = map[string]bool{}
		}
		by[n][x.Scope+" "+x.Name] = true
	}
	out := map[string][]string{}
	for n, ids := range by {
		if len(ids) > 1 {
			out[n] = slices.Sorted(maps.Keys(ids))
		}
	}
	return out
}

func labelMap(kvs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.String()
	}
	return out
}

func pairs(kv []string) map[string]string {
	if len(kv)%2 != 0 {
		panic(fmt.Sprintf("metrictest: labels — пары ключ-значение, получено %d строк", len(kv)))
	}
	out := make(map[string]string, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func contains(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// PromName — имя метрики в Prometheus после трансляции OTLP по правилам
// UnderscoreEscapingWithSuffixes (OTLP-приёмник Prometheus 3, prometheus-экспортёр
// коллектора) с таблицей единиц времени и объёма: символы вне [a-zA-Z0-9:] делят
// имя на слова, unit дописывается словом, Counter получает _total, Gauge с unit
// «1» — _ratio.
func PromName(name, unit string, kind Kind) string {
	tokens := strings.FieldsFunc(name, func(r rune) bool { return !promChar(r) })
	main, per := unitSuffixes(unit)
	if slices.Contains(tokens, main) {
		main = ""
	}
	if per == "per_" {
		per = ""
	} else {
		per = strings.TrimSuffix(per, "_")
		if slices.Contains(tokens, per) {
			per = ""
		}
	}
	if per != "" {
		main = strings.TrimSuffix(main, "_")
	}
	if main != "" {
		tokens = append(tokens, main)
	}
	if per != "" {
		tokens = append(tokens, per)
	}
	if kind == Counter {
		tokens = append(slices.DeleteFunc(tokens, func(s string) bool { return s == "total" }), "total")
	}
	if unit == "1" && kind == Gauge {
		tokens = append(slices.DeleteFunc(tokens, func(s string) bool { return s == "ratio" }), "ratio")
	}
	out := strings.Join(tokens, "_")
	if out != "" && unicode.IsDigit(rune(out[0])) {
		out = "_" + out
	}
	return out
}

var promUnits = map[string]string{
	"d": "days", "h": "hours", "min": "minutes", "s": "seconds",
	"ms": "milliseconds", "us": "microseconds", "ns": "nanoseconds",
	"By": "bytes", "KiBy": "kibibytes", "MiBy": "mebibytes", "GiBy": "gibibytes",
	"KBy": "kilobytes", "MBy": "megabytes", "GBy": "gigabytes",
	"1": "", "%": "percent",
}

var promPerUnits = map[string]string{"s": "second", "m": "minute", "h": "hour", "d": "day"}

func unitSuffixes(unit string) (main, per string) {
	parts := strings.SplitN(unit, "/", 2)
	if m := strings.TrimSpace(parts[0]); m != "" && !strings.ContainsAny(m, "{}") {
		main = m
		if u, ok := promUnits[m]; ok {
			main = u
		}
	}
	if len(parts) > 1 {
		if p := strings.TrimSpace(parts[1]); p != "" && !strings.ContainsAny(p, "{}") {
			per = p
			if u, ok := promPerUnits[p]; ok {
				per = u
			}
		}
		if per != "" {
			per = "per_" + per
		}
	}
	return cleanUnit(main), cleanUnit(per)
}

// cleanUnit: символы вне [a-zA-Z0-9:] — «_», повторы «_» схлопнуты, ведущий снят.
func cleanUnit(u string) string {
	var b strings.Builder
	for _, r := range u {
		if !promChar(r) {
			r = '_'
		}
		if r == '_' && strings.HasSuffix(b.String(), "_") {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimPrefix(b.String(), "_")
}

func promChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':'
}
