package oidckit

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
	"git.stit.tech/stit-core/golibinfra/testkit/oidctest"
)

type series struct{ name, stage, result string }

// seriesOf — имя метрики плюс её labels; любой label сверх stage/result — это
// потенциально неограниченное множество значений на дашборде.
func seriesOf(t *testing.T, name string, labels map[string]string) series {
	t.Helper()
	s := series{name: name}
	for k, v := range labels {
		switch k {
		case "stage":
			s.stage = v
		case "result":
			s.result = v
		default:
			t.Errorf("метрика %s: посторонний label %s=%v", name, k, v)
		}
	}
	return s
}

// Имена метрик, их labels и бакеты — public API кита (README § Observability):
// переименование или потеря бакетов не должны проходить гейт молча.
func TestMetricsEmitted(t *testing.T) {
	ctx := t.Context()
	before := metrictest.Collect(t)

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

	got := map[series]bool{}
	for _, m := range metrictest.Collect(t).Sub(before).All() {
		if m.Scope != otelkit.Scope("oidckit") {
			continue
		}
		if m.Kind != metrictest.Counter && m.Kind != metrictest.Histogram {
			t.Errorf("метрика %s: неожиданный тип %s", m.Name, m.Kind)
		}
		got[seriesOf(t, m.Name, m.Labels)] = true
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

	disc := metrictest.Collect(t).Sub(before).Get(t, "oidc_discovery_duration_seconds", "result", "ok")
	if disc.Unit != "s" {
		t.Errorf("oidc_discovery_duration_seconds: unit=%q, ожидали s", disc.Unit)
	}
	// Дефолтные бакеты SDK рассчитаны на миллисекунды; границы китов — с 5 мс.
	if len(disc.Bounds) == 0 || disc.Bounds[0] != 0.005 {
		t.Errorf("oidc_discovery_duration_seconds без бакетов кита: %v", disc.Bounds)
	}
}
