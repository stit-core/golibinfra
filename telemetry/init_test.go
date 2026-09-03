package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

func TestOTLPHeaders(t *testing.T) {
	t.Parallel()

	if got := otlpHeaders(Config{}); got != nil {
		t.Fatalf("empty token: %v", got)
	}
	got := otlpHeaders(Config{OTLPBearerToken: "abc123"})
	if got["Authorization"] != "Bearer abc123" {
		t.Fatalf("header = %q", got["Authorization"])
	}
}

func TestSignalURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		endpoint, signal, want string
	}{
		{"http://localhost:4318", "v1/traces", "http://localhost:4318/v1/traces"},
		{"https://otlp.example.com/", "v1/metrics", "https://otlp.example.com/v1/metrics"},
		{"https://otlp.example.com/otlp", "v1/logs", "https://otlp.example.com/otlp/v1/logs"},
	}
	for _, c := range cases {
		got, err := signalURL(c.endpoint, c.signal)
		if err != nil || got != c.want {
			t.Errorf("signalURL(%q, %q) = %q, %v; want %q", c.endpoint, c.signal, got, err, c.want)
		}
	}
	for _, bad := range []string{"localhost:4318", "collector", "ftp://x", "http://"} {
		if _, err := signalURL(bad, "v1/traces"); !errors.Is(err, ErrConfig) {
			t.Errorf("signalURL(%q) err = %v, want ErrConfig", bad, err)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	ok := Config{ServiceName: "svc", OTLPEndpoint: "http://localhost:4318", SampleRatio: 1, ExportTimeout: time.Second, TraceBatchDelay: time.Second, MetricInterval: time.Second, LogBatchDelay: time.Second}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := (Config{Disabled: true}).Validate(); err != nil {
		t.Fatalf("disabled needs nothing: %v", err)
	}
	bad := map[string]Config{
		"no service name": func() Config { c := ok; c.ServiceName = ""; return c }(),
		"ratio > 1":       func() Config { c := ok; c.SampleRatio = 2; return c }(),
		"zero timeout":    func() Config { c := ok; c.ExportTimeout = 0; return c }(),
		"host:port":       func() Config { c := ok; c.OTLPEndpoint = "localhost:4318"; return c }(),
	}
	for name, c := range bad {
		if err := c.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v, want ErrConfig", name, err)
		}
	}
}

func TestInitDisabled_StillInstallsPropagatorAndGlobals(t *testing.T) {
	restoreGlobals(t)

	p, err := Init(t.Context(), Config{Disabled: true})
	if err != nil {
		t.Fatalf("Init(disabled) error: %v", err)
	}
	if p.TracerProvider == nil || p.MeterProvider == nil || p.LoggerProvider == nil {
		t.Fatal("Init(disabled) должен вернуть non-nil провайдеры")
	}
	fields := otel.GetTextMapPropagator().Fields()
	if !slices.Contains(fields, "traceparent") || !slices.Contains(fields, "baggage") {
		t.Fatalf("propagator fields = %v, want traceparent+baggage", fields)
	}
	if global.GetLoggerProvider() != log.LoggerProvider(p.LoggerProvider) {
		t.Fatal("global logger provider must be the one from Init")
	}
	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown(disabled) error: %v", err)
	}
}

func TestInitRequiresServiceName(t *testing.T) {
	if _, err := Init(t.Context(), Config{OTLPEndpoint: "http://localhost:4318"}); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

type collector struct {
	mu    sync.Mutex
	paths []string
	auth  map[string]bool
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.auth[r.Header.Get("Authorization")] = true
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func TestInit_ExportsAllSignalsToSpecPaths(t *testing.T) {
	restoreGlobals(t)

	col := &collector{auth: map[string]bool{}}
	srv := httptest.NewServer(col)
	t.Cleanup(srv.Close)

	cfg := Config{
		ServiceName:     "svc",
		OTLPEndpoint:    srv.URL + "/base/",
		OTLPBearerToken: "tok",
		SampleRatio:     1,
		ExportTimeout:   5 * time.Second,
		TraceBatchDelay: time.Hour,
		MetricInterval:  time.Hour,
		LogBatchDelay:   time.Hour,
	}
	p, err := Init(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, end := Span(context.Background(), "job")
	end(nil)
	var rec log.Record
	rec.SetBody(log.StringValue("hello"))
	global.GetLoggerProvider().Logger("test").Emit(context.Background(), rec)

	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	col.mu.Lock()
	defer col.mu.Unlock()
	for _, want := range []string{"/base/v1/traces", "/base/v1/metrics", "/base/v1/logs"} {
		if !slices.Contains(col.paths, want) {
			t.Errorf("collector never saw %s; got %v", want, col.paths)
		}
	}
	if !col.auth["Bearer tok"] || len(col.auth) != 1 {
		t.Errorf("auth headers = %v, want only Bearer tok", col.auth)
	}
}

func restoreGlobals(t *testing.T) {
	t.Helper()
	tp, mp, lp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), global.GetLoggerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		global.SetLoggerProvider(lp)
		otel.SetTextMapPropagator(prop)
	})
}
