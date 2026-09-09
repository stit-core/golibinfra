package amocrmkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installMeter подменяет глобальный MeterProvider in-memory SDK: инструменты кит
// берёт из него в New, поэтому клиент создаётся после вызова.
func installMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(sdkmetric.NewMeterProvider()) })
	return reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	return rm
}

func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

// counterSum суммирует datapoints счётчика name, чьи атрибуты содержат want
// (nil want — все серии метрики).
func counterSum(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) int64 {
	t.Helper()
	var total int64
	for _, sm := range collect(t, reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want Sum[int64]", name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				if attrsMatch(dp.Attributes, want) {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func histogram(t *testing.T, reader *sdkmetric.ManualReader, name string, want map[string]string) (count uint64, bounds []float64) {
	t.Helper()
	for _, sm := range collect(t, reader).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is %T, want Histogram[float64]", name, m.Data)
			}
			for _, dp := range h.DataPoints {
				if attrsMatch(dp.Attributes, want) {
					count += dp.Count
					bounds = dp.Bounds
				}
			}
		}
	}
	return count, bounds
}

// Имена метрик и значения labels — public API кита: дашборды потребителя ломает
// молча и опечатка в них, и потерянный label.
func TestMetrics_SuccessfulCall(t *testing.T) {
	reader := installMeter(t)
	rec := newRecordingServer(t, `{"_embedded":{"leads":[]}}`)
	c := newTestClient(t, rec.URL)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil {
		t.Fatalf("ListLeads: %v", err)
	}

	if got := counterSum(t, reader, "amocrm_api_calls_total", map[string]string{"endpoint": "leads.list", "status": "200"}); got != 1 {
		t.Errorf("amocrm_api_calls_total{endpoint=leads.list,status=200} = %d, want 1", got)
	}
	count, bounds := histogram(t, reader, "amocrm_api_duration_seconds", map[string]string{"endpoint": "leads.list"})
	if count != 1 {
		t.Errorf("amocrm_api_duration_seconds{endpoint=leads.list} count = %d, want 1", count)
	}
	if len(bounds) == 0 || bounds[len(bounds)-1] != 30 {
		t.Errorf("duration bounds = %v, want otelkit.DurationBuckets()", bounds)
	}
	if got := counterSum(t, reader, "amocrm_rate_limited_total", nil); got != 0 {
		t.Errorf("amocrm_rate_limited_total = %d, want 0 on a successful call", got)
	}
}

// 429 от amoCRM — result="remote"; аккаунт в labels не попадает, только tld.
func TestMetrics_RemoteRateLimit(t *testing.T) {
	reader := installMeter(t)
	srv := newStatusServer(t, http.StatusTooManyRequests, `{"retry_after":3}`)
	c := newTestClient(t, srv)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("ListLeads must fail on 429")
	}

	if got := counterSum(t, reader, "amocrm_rate_limited_total", map[string]string{"tld": "ru", "result": "remote"}); got != 1 {
		t.Errorf("amocrm_rate_limited_total{tld=ru,result=remote} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "amocrm_api_calls_total", map[string]string{"endpoint": "leads.list", "status": "429"}); got != 1 {
		t.Errorf("amocrm_api_calls_total{status=429} = %d, want 1", got)
	}
}

// Отклонённый лимитером вызов до сети не доходит: считается только как
// result="local", api_calls_total и гистограмма молчат.
func TestMetrics_LocalRateLimit(t *testing.T) {
	reader := installMeter(t)
	rec := newRecordingServer(t, `{}`)
	c := newTestClientCfg(t, Config{RateRPS: 1000, MaxWait: time.Second, HostOverride: rec.URL}, testHTTPConfig())
	c.rl.penalize(testAccount().ref().key(), time.Hour)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("ListLeads must fail while the account is paused")
	}
	if rec.hits != 0 {
		t.Fatalf("server got %d requests from a paused account", rec.hits)
	}

	if got := counterSum(t, reader, "amocrm_rate_limited_total", map[string]string{"tld": "ru", "result": "local"}); got != 1 {
		t.Errorf("amocrm_rate_limited_total{tld=ru,result=local} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "amocrm_api_calls_total", nil); got != 0 {
		t.Errorf("amocrm_api_calls_total = %d, want 0 — the call never reached the network", got)
	}
	if count, _ := histogram(t, reader, "amocrm_api_duration_seconds", nil); count != 0 {
		t.Errorf("amocrm_api_duration_seconds count = %d, want 0", count)
	}
}

// grant различает подключение аккаунта и продление токена, status — исход.
func TestMetrics_OAuthGrantAndStatus(t *testing.T) {
	reader := installMeter(t)
	status := http.StatusOK
	srv := newHandlerServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"title":"Unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":86400,"server_time":1}`))
	})
	c := newTestClient(t, srv)
	ctx := context.Background()

	if _, err := c.ExchangeCode(ctx, testAccount(), testOAuthConfig(), "code"); err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	status = http.StatusUnauthorized
	if _, err := c.RefreshToken(ctx, testAccount(), testOAuthConfig(), "refresh"); err == nil {
		t.Fatal("RefreshToken must fail on 401")
	}

	if got := counterSum(t, reader, "amocrm_oauth_refresh_total", map[string]string{"grant": "authorization_code", "status": "success"}); got != 1 {
		t.Errorf("amocrm_oauth_refresh_total{grant=authorization_code,status=success} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "amocrm_oauth_refresh_total", map[string]string{"grant": "refresh_token", "status": "error"}); got != 1 {
		t.Errorf("amocrm_oauth_refresh_total{grant=refresh_token,status=error} = %d, want 1", got)
	}
}

// Отказ до сети (координаты аккаунта, dot-сегмент) не пишет ни одной серии.
func TestMetrics_RejectedBeforeNetworkWritesNothing(t *testing.T) {
	reader := installMeter(t)
	rec := newRecordingServer(t, `{}`)
	c := newTestClient(t, rec.URL)
	ctx := context.Background()

	if _, err := c.ListLeads(ctx, Account{Subdomain: "", TLD: "ru"}, ListParams{}); err == nil {
		t.Fatal("ListLeads must reject an empty subdomain")
	}
	if _, err := c.GetFileLinks(ctx, testAccount(), ".."); err == nil {
		t.Fatal("GetFileLinks must reject a dot segment")
	}

	for _, name := range []string{"amocrm_api_calls_total", "amocrm_rate_limited_total", "amocrm_oauth_refresh_total"} {
		if got := counterSum(t, reader, name, nil); got != 0 {
			t.Errorf("%s = %d, want 0", name, got)
		}
	}
	if count, _ := histogram(t, reader, "amocrm_api_duration_seconds", nil); count != 0 {
		t.Errorf("amocrm_api_duration_seconds count = %d, want 0", count)
	}
}

// newHandlerServer поднимает httptest-сервер и отдаёт его URL для HostOverride.
func newHandlerServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func newStatusServer(t *testing.T, status int, body string) string {
	t.Helper()
	return newHandlerServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}
