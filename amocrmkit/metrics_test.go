package amocrmkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// metricsSince — дельта метрик процесса от момента вызова (metrictest).
func metricsSince(t *testing.T) func() metrictest.Snapshot {
	t.Helper()
	before := metrictest.Collect(t)
	return func() metrictest.Snapshot {
		t.Helper()
		return metrictest.Collect(t).Sub(before)
	}
}

// counter — значение серии name с ровно этими labels. Тип инструмента — часть контракта: rate()
// дашборда потребителя молча ломает UpDownCounter или gauge под тем же именем.
func counter(t *testing.T, d metrictest.Snapshot, name string, labels ...string) float64 {
	t.Helper()
	s := d.Get(t, name, labels...)
	if s.Kind != metrictest.Counter {
		t.Errorf("%s: instrument kind %q, want %q", name, s.Kind, metrictest.Counter)
	}
	return s.Value
}

// histogram — число наблюдений и границы гистограммы name по сериям с labels.
func histogram(t *testing.T, d metrictest.Snapshot, name string, labels ...string) (count uint64, bounds []float64) {
	t.Helper()
	for _, s := range d.Find(name, labels...) {
		if s.Kind != metrictest.Histogram || s.Unit != "s" {
			t.Errorf("%s: instrument kind %q, unit %q; want %q in seconds", name, s.Kind, s.Unit, metrictest.Histogram)
		}
		count += s.Count
		bounds = s.Bounds
	}
	return count, bounds
}

// Имена метрик и значения labels — public API кита: дашборды потребителя ломает
// молча и опечатка в них, и потерянный label.
func TestMetrics_SuccessfulCall(t *testing.T) {
	since := metricsSince(t)
	rec := newRecordingServer(t, `{"_embedded":{"leads":[]}}`)
	c := newTestClient(t, rec.URL)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err != nil {
		t.Fatalf("ListLeads: %v", err)
	}

	if got := counter(t, since(), "amocrm_api_calls_total", "endpoint", "leads.list", "status", "200"); got != 1 {
		t.Errorf("amocrm_api_calls_total{endpoint=leads.list,status=200} = %v, want 1", got)
	}
	count, bounds := histogram(t, since(), "amocrm_api_duration_seconds", "endpoint", "leads.list")
	if count != 1 {
		t.Errorf("amocrm_api_duration_seconds{endpoint=leads.list} count = %d, want 1", count)
	}
	if len(bounds) == 0 || bounds[len(bounds)-1] != 30 {
		t.Errorf("duration bounds = %v, want otelkit.DurationBuckets()", bounds)
	}
	if got := since().Value("amocrm_rate_limited_total"); got != 0 {
		t.Errorf("amocrm_rate_limited_total = %v, want 0 on a successful call", got)
	}
}

// 429 от amoCRM — result="remote"; аккаунт в labels не попадает, только tld.
func TestMetrics_RemoteRateLimit(t *testing.T) {
	since := metricsSince(t)
	srv := newStatusServer(t, http.StatusTooManyRequests, `{"retry_after":3}`)
	c := newTestClient(t, srv)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("ListLeads must fail on 429")
	}

	if got := counter(t, since(), "amocrm_rate_limited_total", "tld", "ru", "result", "remote"); got != 1 {
		t.Errorf("amocrm_rate_limited_total{tld=ru,result=remote} = %v, want 1", got)
	}
	if got := counter(t, since(), "amocrm_api_calls_total", "endpoint", "leads.list", "status", "429"); got != 1 {
		t.Errorf("amocrm_api_calls_total{status=429} = %v, want 1", got)
	}
}

// Отклонённый лимитером вызов до сети не доходит: считается только как
// result="local", api_calls_total и гистограмма молчат.
func TestMetrics_LocalRateLimit(t *testing.T) {
	since := metricsSince(t)
	rec := newRecordingServer(t, `{}`)
	c := newTestClientCfg(t, Config{RateRPS: 1000, MaxWait: time.Second, HostOverride: rec.URL}, testHTTPConfig())
	c.rl.penalize(testAccount().ref().key(), time.Hour)

	if _, err := c.ListLeads(context.Background(), testAccount(), ListParams{}); err == nil {
		t.Fatal("ListLeads must fail while the account is paused")
	}
	if rec.hits != 0 {
		t.Fatalf("server got %d requests from a paused account", rec.hits)
	}

	if got := counter(t, since(), "amocrm_rate_limited_total", "tld", "ru", "result", "local"); got != 1 {
		t.Errorf("amocrm_rate_limited_total{tld=ru,result=local} = %v, want 1", got)
	}
	if got := since().Value("amocrm_api_calls_total"); got != 0 {
		t.Errorf("amocrm_api_calls_total = %v, want 0 — the call never reached the network", got)
	}
	if count, _ := histogram(t, since(), "amocrm_api_duration_seconds"); count != 0 {
		t.Errorf("amocrm_api_duration_seconds count = %d, want 0", count)
	}
}

// grant различает подключение аккаунта и продление токена, status — исход.
func TestMetrics_OAuthGrantAndStatus(t *testing.T) {
	since := metricsSince(t)
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

	if got := counter(t, since(), "amocrm_oauth_refresh_total", "grant", "authorization_code", "status", "success"); got != 1 {
		t.Errorf("amocrm_oauth_refresh_total{grant=authorization_code,status=success} = %v, want 1", got)
	}
	if got := counter(t, since(), "amocrm_oauth_refresh_total", "grant", "refresh_token", "status", "error"); got != 1 {
		t.Errorf("amocrm_oauth_refresh_total{grant=refresh_token,status=error} = %v, want 1", got)
	}
}

// Отказ до сети (координаты аккаунта, dot-сегмент) не пишет ни одной серии.
func TestMetrics_RejectedBeforeNetworkWritesNothing(t *testing.T) {
	since := metricsSince(t)
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
		if got := since().Value(name); got != 0 {
			t.Errorf("%s = %v, want 0", name, got)
		}
	}
	if count, _ := histogram(t, since(), "amocrm_api_duration_seconds"); count != 0 {
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
