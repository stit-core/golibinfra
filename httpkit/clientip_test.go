package httpkit_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.stit.tech/stit-core/golibinfra/httpkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

// clientIPOf прогоняет запрос через ClientIP и отдаёт то, что увидел хендлер.
func clientIPOf(t *testing.T, mw func(http.Handler) http.Handler, remote string, xff ...string) string {
	t.Helper()
	var got string
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = httpkit.ClientIPFrom(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	req.Header.Set("X-Real-IP", "198.51.100.66")
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// HOPS — сколько прокси дописывают X-Forwarded-For: берётся их запись, левее — клиент; X-Real-IP
// не читается, короткая или битая цепочка — адрес соединения.
func TestClientIP_TrustProxyHops(t *testing.T) {
	cases := []struct {
		name   string
		hops   int
		remote string
		xff    []string
		want   string
	}{
		{"без прокси — адрес соединения", 0, "192.0.2.10:5555", []string{"203.0.113.7"}, "192.0.2.10"},
		{"один прокси — правая запись", 1, "10.0.0.9:1", []string{"6.6.6.6, 203.0.113.7"}, "203.0.113.7"},
		{"прокси добавил свой заголовок", 1, "10.0.0.9:1", []string{"6.6.6.6", "203.0.113.7"}, "203.0.113.7"},
		{"CDN → nginx", 2, "10.0.0.9:1", []string{"6.6.6.6, 203.0.113.7, 198.51.100.7"}, "203.0.113.7"},
		{"цепочка короче HOPS", 2, "10.0.0.9:1", []string{"203.0.113.7"}, "10.0.0.9"},
		{"запись прокси — не IP", 1, "10.0.0.9:1", []string{"203.0.113.7, not-an-ip"}, "10.0.0.9"},
		{"без X-Forwarded-For", 1, "10.0.0.9:1", nil, "10.0.0.9"},
		{"IPv4-mapped — IPv4", 1, "10.0.0.9:1", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"IPv6 — каноническая запись", 1, "10.0.0.9:1", []string{"2001:DB8::1"}, "2001:db8::1"},
		{"адрес соединения IPv6", 0, "[2001:db8::9]:443", nil, "2001:db8::9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clientIPOf(t, httpkit.ClientIP(logkit.Nop(), c.hops), c.remote, c.xff...); got != c.want {
				t.Fatalf("ClientIPFrom = %q, want %q", got, c.want)
			}
		})
	}
	if got := httpkit.ClientIPFrom(context.Background()); got != "" {
		t.Fatalf("without ClientIP in the chain: %q", got)
	}
}

// За прокси без HTTP_TRUST_PROXY_HOPS все клиенты делят его адрес — одна строка Warn, не на
// каждый запрос.
func TestClientIP_WarnsOnceAboutIgnoredForwardedFor(t *testing.T) {
	var buf bytes.Buffer
	log, err := logkit.New(logkit.Config{Level: "info"}, logkit.WithWriter(&buf), logkit.WithoutOTel())
	if err != nil {
		t.Fatal(err)
	}
	mw := httpkit.ClientIP(log, 0)
	clientIPOf(t, mw, "10.0.0.9:1")
	if buf.Len() != 0 {
		t.Fatalf("no X-Forwarded-For — no warning: %s", buf.String())
	}
	for range 3 {
		clientIPOf(t, mw, "10.0.0.9:1", "203.0.113.7")
	}
	if n := strings.Count(buf.String(), "HTTP_TRUST_PROXY_HOPS"); n != 1 || !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("want one Warn about HTTP_TRUST_PROXY_HOPS, got %d: %s", n, buf.String())
	}
}
