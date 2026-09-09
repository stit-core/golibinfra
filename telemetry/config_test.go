package telemetry

import (
	"errors"
	"maps"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ServiceName:     "svc",
		OTLPEndpoint:    "http://localhost:4318",
		SampleRatio:     1,
		ExportTimeout:   time.Second,
		TraceBatchDelay: time.Second,
		MetricInterval:  time.Second,
		LogBatchDelay:   time.Second,
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
		{"https://otlp.example.com/otlp/", "v1/traces", "https://otlp.example.com/otlp/v1/traces"},
	}
	for _, c := range cases {
		got, err := signalURL(c.endpoint, c.signal)
		if err != nil || got != c.want {
			t.Errorf("signalURL(%q, %q) = %q, %v; want %q", c.endpoint, c.signal, got, err, c.want)
		}
	}
	bad := []string{
		"localhost:4318", // no scheme
		"collector",
		"ftp://x",
		"http://",  // no host
		"http://[", // url.Parse fails
		"",
	}
	for _, b := range bad {
		if _, err := signalURL(b, "v1/traces"); !errors.Is(err, ErrConfig) {
			t.Errorf("signalURL(%q) err = %v, want ErrConfig", b, err)
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := (Config{Disabled: true}).Validate(); err != nil {
		t.Fatalf("disabled needs nothing: %v", err)
	}
	bad := map[string]func(*Config){
		"no service name":    func(c *Config) { c.ServiceName = "" },
		"ratio > 1":          func(c *Config) { c.SampleRatio = 2 },
		"negative ratio":     func(c *Config) { c.SampleRatio = -0.1 },
		"zero export":        func(c *Config) { c.ExportTimeout = 0 },
		"zero trace delay":   func(c *Config) { c.TraceBatchDelay = 0 },
		"zero metric period": func(c *Config) { c.MetricInterval = 0 },
		"zero log delay":     func(c *Config) { c.LogBatchDelay = 0 },
		"host:port":          func(c *Config) { c.OTLPEndpoint = "localhost:4318" },
		"unparsable url":     func(c *Config) { c.OTLPEndpoint = "http://[::1" },
		"bad header name":    func(c *Config) { c.OTLPHeaders = Headers{"bad key": "v"} },
		"empty header name":  func(c *Config) { c.OTLPHeaders = Headers{"": "v"} },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if err := c.Validate(); !errors.Is(err, ErrConfig) {
				t.Errorf("err = %v, want ErrConfig", err)
			}
		})
	}
}

func TestHeaders_Decode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want Headers
	}{
		{"api-key=secret", Headers{"api-key": "secret"}},
		{"api-key=secret,x-scope-orgid=acme", Headers{"api-key": "secret", "x-scope-orgid": "acme"}},
		{" api-key = sec%20ret ", Headers{"api-key": "sec ret"}},
		{"a=1,,b=2", Headers{"a": "1", "b": "2"}},
		{"a=", Headers{"a": ""}},
		{"", Headers{}},
	}
	for _, c := range cases {
		var got Headers
		if err := got.Decode(c.in); err != nil {
			t.Errorf("Decode(%q): %v", c.in, err)
			continue
		}
		if !maps.Equal(got, c.want) {
			t.Errorf("Decode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"nokey", "=value", "a=%zz"} {
		var got Headers
		if err := got.Decode(bad); !errors.Is(err, ErrConfig) {
			t.Errorf("Decode(%q) err = %v, want ErrConfig", bad, err)
		}
	}
}

func TestOTLPHeaders(t *testing.T) {
	t.Parallel()

	if got := otlpHeaders(Config{}); got != nil {
		t.Fatalf("no headers, no token: %v", got)
	}
	if got := otlpHeaders(Config{OTLPBearerToken: "abc123"}); got["Authorization"] != "Bearer abc123" {
		t.Fatalf("header = %q", got["Authorization"])
	}
	// Заголовки из env доезжают и без токена.
	got := otlpHeaders(Config{OTLPHeaders: Headers{"x-scope-orgid": "acme"}})
	if got["X-Scope-Orgid"] != "acme" {
		t.Fatalf("env headers = %v", got)
	}
	// Регрессия: токен переопределяет Authorization из env, а не дублирует его.
	got = otlpHeaders(Config{
		OTLPHeaders:     Headers{"authorization": "Bearer stale", "x-scope-orgid": "acme"},
		OTLPBearerToken: "fresh",
	})
	if len(got) != 2 || got["Authorization"] != "Bearer fresh" || got["X-Scope-Orgid"] != "acme" {
		t.Fatalf("merged headers = %v", got)
	}
}

func TestNewConfig(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "svc")
	t.Setenv("OTEL_SERVICE_VERSION", "v1.2.3")
	t.Setenv("APP_ENV", "prod")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otlp.example.com/otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-scope-orgid=acme")
	t.Setenv("OTEL_TOKEN", "tok")

	c, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if c.ServiceName != "svc" || c.ServiceVersion != "v1.2.3" || c.Environment != "prod" {
		t.Fatalf("identity: %+v", c)
	}
	if c.OTLPEndpoint != "https://otlp.example.com/otlp" || c.OTLPBearerToken != "tok" {
		t.Fatalf("endpoint/token: %+v", c)
	}
	if c.OTLPHeaders["x-scope-orgid"] != "acme" {
		t.Fatalf("headers: %+v", c.OTLPHeaders)
	}
	if c.Disabled {
		t.Fatal("OTEL_SDK_DISABLED unset must leave telemetry enabled")
	}
	want := Config{ExportTimeout: 10 * time.Second, TraceBatchDelay: 5 * time.Second, MetricInterval: 10 * time.Second, LogBatchDelay: time.Second}
	if c.ExportTimeout != want.ExportTimeout || c.TraceBatchDelay != want.TraceBatchDelay ||
		c.MetricInterval != want.MetricInterval || c.LogBatchDelay != want.LogBatchDelay || c.SampleRatio != 1 {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestNewConfig_Disabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	c, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if !c.Disabled {
		t.Fatal("OTEL_SDK_DISABLED=true must disable the SDK")
	}
}

func TestNewConfig_InvalidEnv(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "svc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318")
	if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_ReportsEnvconfigError(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "maybe")
	if _, err := NewConfig(); err == nil {
		t.Fatal("NewConfig must fail on a non-boolean OTEL_SDK_DISABLED")
	}
}
