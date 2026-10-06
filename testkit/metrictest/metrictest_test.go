package metrictest

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// early создан при инициализации переменных пакета, до первого Collect: делегат OTel
// обязан перенести его на провайдер процесса.
var early, _ = otel.Meter("metrictest/early").Int64Counter("metrictest_early_total")

// atStart — глобальный MeterProvider после инициализации пакетов, до тестов.
var atStart metric.MeterProvider

func TestMain(m *testing.M) {
	atStart = otel.GetMeterProvider()
	os.Exit(m.Run())
}

type stop struct{}

type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	panic(stop{})
}

// fatalOf — сообщение Fatalf, которым fn остановил тест; пусто, если не остановил.
func fatalOf(t *testing.T, fn func(testing.TB)) (msg string) {
	t.Helper()
	tb := &fatalTB{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stop); !ok {
				panic(r)
			}
			msg = tb.msg
		}
	}()
	fn(tb)
	return ""
}

// Импорт пакета глобальный провайдер не ставит: иначе в бинаре, где тесты берут
// NewProvider, проверка «граф поставил глобальный MeterProvider» проходила бы на
// провайдере metrictest.
func TestImport_LeavesGlobalProvider(t *testing.T) {
	if _, sdk := atStart.(*sdkmetric.MeterProvider); sdk {
		t.Fatal("importing metrictest installed a global MeterProvider")
	}
}

// Провайдер теста — свой у подтеста: параллельные подтесты одной метрики с одними
// labels видят только свои записи, без дельт.
func TestNewProvider_IsolatesParallelSubtests(t *testing.T) {
	for _, result := range []string{"ok", "invalid", "invalid"} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			p := NewProvider()
			if otel.GetMeterProvider() == p.MeterProvider {
				t.Fatal("NewProvider installed itself as the global MeterProvider")
			}
			logins, err := p.Meter("metrictest/parallel").Int64Counter("parallel_logins_total")
			if err != nil {
				t.Fatal(err)
			}
			logins.Add(t.Context(), 1, metric.WithAttributes(attribute.String("result", result)))
			got := p.Collect(t).Find("parallel_logins_total")
			if len(got) != 1 || got[0].Labels["result"] != result || got[0].Value != 1 {
				t.Fatalf("series of the subtest provider: %v, want only result=%s = 1", got, result)
			}
		})
	}
}

func TestCollect_InstrumentCreatedBeforeInstall(t *testing.T) {
	before := Collect(t)
	early.Add(t.Context(), 2)
	if got := Collect(t).Sub(before).Value("metrictest_early_total"); got != 2 {
		t.Fatalf("metrictest_early_total delta = %v, want 2", got)
	}
}

// Тесты снимков — на провайдере теста: серии прошлых прогонов (-count) в них не попадают.
func TestSnapshot_KindsUnitsLabelsValues(t *testing.T) {
	p := NewProvider()
	m := p.Meter("metrictest/kinds")
	ctx := t.Context()
	counter, _ := m.Int64Counter("kinds_requests_total")
	updown, _ := m.Int64UpDownCounter("kinds_inflight")
	gauge, _ := m.Float64Gauge("kinds_temperature", metric.WithUnit("Cel"))
	hist, _ := m.Float64Histogram("kinds_duration_seconds", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.1, 1))

	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
	before := p.Collect(t)
	counter.Add(ctx, 2, metric.WithAttributes(attribute.String("result", "ok")))
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "error"), attribute.Int("code", 503)))
	updown.Add(ctx, 3)
	updown.Add(ctx, -1)
	gauge.Record(ctx, 21.5)
	hist.Record(ctx, 0.5, metric.WithAttributes(attribute.String("op", "get")))
	hist.Record(ctx, 2, metric.WithAttributes(attribute.String("op", "get")))
	d := p.Collect(t).Sub(before)

	if s := d.Get(t, "kinds_requests_total", "result", "ok"); s.Kind != Counter || s.Value != 2 || s.Scope != "metrictest/kinds" {
		t.Errorf("counter: %+v, want counter delta 2 in scope metrictest/kinds", s)
	}
	if s := d.Get(t, "kinds_requests_total", "result", "error", "code", "503"); s.Value != 1 {
		t.Errorf("counter with int label: %v", s)
	}
	if s := d.Get(t, "kinds_inflight"); s.Kind != UpDownCounter || s.Value != 2 {
		t.Errorf("updown: %v (%s), want 2", s, s.Kind)
	}
	if s := d.Get(t, "kinds_temperature"); s.Kind != Gauge || s.Unit != "Cel" || s.Value != 21.5 {
		t.Errorf("gauge: %+v", s)
	}
	s := d.Get(t, "kinds_duration_seconds", "op", "get")
	if s.Kind != Histogram || s.Unit != "s" || s.Count != 2 || s.Value != 2.5 || !slices.Equal(s.Bounds, []float64{0.1, 1}) {
		t.Errorf("histogram: %+v", s)
	}
	if got := d.Value("kinds_requests_total"); got != 3 {
		t.Errorf("Value across series = %v, want 3", got)
	}
	if got := len(d.Find("kinds_requests_total", "result", "error")); got != 1 {
		t.Errorf("Find by label subset: %d series, want 1", got)
	}
	if got := d.All(); len(got) != 5 {
		t.Errorf("All: %v, want 5 series", got)
	}
}

func TestSub_DropsUnchangedKeepsNewAndGauges(t *testing.T) {
	p := NewProvider()
	m := p.Meter("metrictest/sub")
	ctx := t.Context()
	idle, _ := m.Int64Counter("sub_idle_total")
	level, _ := m.Int64Gauge("sub_level")
	inflight, _ := m.Int64UpDownCounter("sub_inflight")
	old := metric.WithAttributes(attribute.String("queue", "old"))
	idle.Add(ctx, 5)
	level.Record(ctx, 7)
	inflight.Add(ctx, 1, old)
	inflight.Add(ctx, -1, old)

	before := p.Collect(t)
	inflight.Add(ctx, 1)
	inflight.Add(ctx, -1)
	inflight.Add(ctx, 1, old)
	inflight.Add(ctx, -1, old)
	d := p.Collect(t).Sub(before)
	if got := d.Find("sub_idle_total"); len(got) != 0 {
		t.Errorf("unchanged counter in delta: %v", got)
	}
	if got := d.Get(t, "sub_inflight"); got.Value != 0 {
		t.Errorf("new series must stay in delta with its value, got %v", got)
	}
	if got := d.Find("sub_inflight", "queue", "old"); len(got) != 0 {
		t.Errorf("series of before unchanged by +1 −1 must leave the delta, got %v", got)
	}
	if got := d.Get(t, "sub_level"); got.Value != 7 {
		t.Errorf("gauge in delta = %v, want current value 7", got)
	}
	if got := d.Value("sub_absent_total"); got != 0 {
		t.Errorf("absent series value = %v, want 0", got)
	}
}

func TestGet_MissingSeriesFails(t *testing.T) {
	p := NewProvider()
	c, _ := p.Meter("metrictest/get").Int64Counter("get_calls_total")
	c.Add(t.Context(), 1, metric.WithAttributes(attribute.String("result", "ok"), attribute.String("extra", "x")))
	snap := p.Collect(t)
	msg := fatalOf(t, func(tb testing.TB) { snap.Get(tb, "get_calls_total", "result", "ok") })
	if !strings.Contains(msg, "нет серии get_calls_total") || !strings.Contains(msg, "extra=x") {
		t.Fatalf("Get must require the exact label set and list the metric's series, got %q", msg)
	}
}

// telemetry.Init в том же бинаре подменяет провайдер процесса: Collect падает, а не
// читает провайдер, в который код больше не пишет.
func TestCollect_ProviderReplacedFails(t *testing.T) {
	Collect(t)
	otel.SetMeterProvider(sdkmetric.NewMeterProvider())
	defer otel.SetMeterProvider(process.MeterProvider)
	if msg := fatalOf(t, func(tb testing.TB) { Collect(tb) }); !strings.Contains(msg, "подменён") {
		t.Fatalf("replaced provider must fail Collect, got %q", msg)
	}
}

func TestPromName(t *testing.T) {
	cases := []struct {
		name, unit string
		kind       Kind
		want       string
	}{
		{"http_client_request_duration_seconds", "s", Histogram, "http_client_request_duration_seconds"},
		{"http.client.request.duration", "s", Histogram, "http_client_request_duration_seconds"},
		{"log_messages_total", "", Counter, "log_messages_total"},
		{"pg_pool_acquire_duration_seconds", "s", Counter, "pg_pool_acquire_duration_seconds_total"},
		{"http_client_breaker_open", "", UpDownCounter, "http_client_breaker_open"},
		{"nats_stream_seq", "{seq}", Gauge, "nats_stream_seq"},
		{"cpu.utilization", "1", Gauge, "cpu_utilization_ratio"},
		{"payload.size", "By", Histogram, "payload_size_bytes"},
		{"events", "{event}/s", Gauge, "events_per_second"},
		{"2xx.responses", "", Counter, "_2xx_responses_total"},
	}
	for _, c := range cases {
		if got := PromName(c.name, c.unit, c.kind); got != c.want {
			t.Errorf("PromName(%q, %q, %s) = %q, want %q", c.name, c.unit, c.kind, got, c.want)
		}
	}
}

// Пара kit-гистограммы и гистограммы otelhttp с одним именем в Prometheus.
func TestCollisions_SameHistogramNameFromTwoScopes(t *testing.T) {
	ctx := t.Context()
	p := NewProvider()
	kit, _ := p.Meter("metrictest/kit").Float64Histogram("http_client_request_duration_seconds", metric.WithUnit("s"))
	contrib, _ := p.Meter("metrictest/contrib").Float64Histogram("http.client.request.duration", metric.WithUnit("s"))
	kit.Record(ctx, 0.1)
	contrib.Record(ctx, 0.1)

	got := p.Collect(t).Collisions()["http_client_request_duration_seconds"]
	want := []string{
		"metrictest/contrib http.client.request.duration",
		"metrictest/kit http_client_request_duration_seconds",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("collision = %v, want %v", got, want)
	}
}
