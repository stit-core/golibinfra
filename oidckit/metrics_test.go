package oidckit

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

type series struct{ name, stage, result string }

// seriesOf — имя метрики плюс её labels; любой label сверх stage/result — это
// потенциально неограниченное множество значений на дашборде.
func seriesOf(t *testing.T, name string, attrs attribute.Set) series {
	t.Helper()
	s := series{name: name}
	for _, kv := range attrs.ToSlice() {
		switch kv.Key {
		case "stage":
			s.stage = kv.Value.AsString()
		case "result":
			s.result = kv.Value.AsString()
		default:
			t.Errorf("метрика %s: посторонний label %s=%v", name, kv.Key, kv.Value.AsString())
		}
	}
	return s
}

// Имена метрик, их labels и бакеты — public API кита (README § Observability):
// переименование или потеря бакетов не должны проходить гейт молча.
func TestMetricsEmitted(t *testing.T) {
	ctx := t.Context()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	idp := oidctest.New(t)
	b := newTestBFF(t, idp, memStore(), Hooks{})

	code, state, flow := loginFlow(t, b, idp, "")
	if _, err := b.callback(ctx, callbackWith(code, state, flow)); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if _, err := b.callback(ctx, callbackWith("code", "state", "чужая")); err == nil {
		t.Fatal("callback с чужой flow-cookie прошёл")
	}
	if _, err := b.logout(ctx, &logoutInput{SecFetchSite: "same-origin"}); err != nil {
		t.Fatalf("logout: %v", err)
	}
	v, err := b.verifier(ctx)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if _, err := v.Verify(ctx, "not.a.token"); err == nil {
		t.Fatal("Verify нечитаемого токена прошёл")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	got := map[series]bool{}
	units := map[string]string{}
	var discoveryBounds []float64
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != otelkit.Scope("oidckit") {
			continue
		}
		for _, m := range sm.Metrics {
			units[m.Name] = m.Unit
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					got[seriesOf(t, m.Name, dp.Attributes)] = true
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					got[seriesOf(t, m.Name, dp.Attributes)] = true
					discoveryBounds = dp.Bounds
				}
			default:
				t.Errorf("метрика %s: неожиданный тип данных %T", m.Name, m.Data)
			}
		}
	}

	for _, want := range []series{
		{name: "oidc_logins_total", stage: stageLogin, result: "ok"},
		{name: "oidc_logins_total", stage: stageCallback, result: "ok"},
		{name: "oidc_logins_total", stage: stageCallback, result: "csrf_mismatch"},
		{name: "oidc_logins_total", stage: stageLogout, result: "ok"},
		{name: "oidc_verifications_total", result: "ok"},
		{name: "oidc_verifications_total", result: "malformed"},
		{name: "oidc_jwks_refresh_total", result: "ok"},
		{name: "oidc_discovery_duration_seconds", result: "ok"},
	} {
		if !got[want] {
			t.Errorf("нет серии %s{stage=%q,result=%q}", want.name, want.stage, want.result)
		}
	}

	if u := units["oidc_discovery_duration_seconds"]; u != "s" {
		t.Errorf("oidc_discovery_duration_seconds: unit=%q, ожидали s", u)
	}
	// Дефолтные бакеты SDK рассчитаны на миллисекунды; границы китов — с 5 мс.
	if len(discoveryBounds) == 0 || discoveryBounds[0] != 0.005 {
		t.Errorf("oidc_discovery_duration_seconds без бакетов кита: %v", discoveryBounds)
	}
}
