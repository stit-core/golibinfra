package anthropickit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
)

func TestNewConfig_ReadsEnvAndAppliesDefaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	want := Config{
		APIKey:       "sk-env",
		BaseURL:      "https://api.anthropic.com",
		DefaultModel: "claude-opus-5",
		MaxRetries:   2,
		Timeout:      10 * time.Minute,
	}
	if cfg != want {
		t.Fatalf("NewConfig = %+v, want %+v", cfg, want)
	}
}

func TestNewConfig_OverridesAndValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	t.Setenv("ANTHROPIC_BASE_URL", "http://gateway.internal:8080")
	t.Setenv("ANTHROPIC_DEFAULT_MODEL", "claude-sonnet-5")
	t.Setenv("ANTHROPIC_MAX_RETRIES", "0")
	t.Setenv("ANTHROPIC_TIMEOUT", "45s")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.BaseURL != "http://gateway.internal:8080" || cfg.DefaultModel != "claude-sonnet-5" ||
		cfg.MaxRetries != 0 || cfg.Timeout != 45*time.Second {
		t.Fatalf("NewConfig = %+v", cfg)
	}

	t.Setenv("ANTHROPIC_BASE_URL", "gateway.internal:8080")
	if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("NewConfig err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_RequiresKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewConfig(); err == nil {
		t.Fatal("NewConfig must fail without ANTHROPIC_API_KEY")
	}
}

func TestModule_ProvidesAPIAndOptionalProbe(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")

	var got API
	app := fxtest.New(
		t,
		fx.Provide(NewConfig),
		fx.Provide(logkit.Nop),
		Module,
		healthkit.Provide(Probe),
		fx.Invoke(func(api API, in struct {
			fx.In
			Probes []healthkit.Probe `group:"healthkit.probes"`
		},
		) {
			got = api
			if len(in.Probes) != 1 || in.Probes[0].Name != probeName {
				t.Errorf("health group = %+v, want the anthropickit probe", in.Probes)
			}
		}),
	)
	app.RequireStart().RequireStop()

	if got == nil || got.DefaultModel() != "claude-opus-5" {
		t.Fatalf("API from the graph = %+v", got)
	}
}

// Граф из docs/usage.md § Подключение через fx Module: пример в доке обязан
// собираться, а не падать на первом fx.New у потребителя.
func TestModule_DocsFxExampleValidates(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")

	if err := fx.ValidateApp(
		fx.Provide(NewConfig, logkit.NewConfig),
		logkit.Module,
		Module,
		healthkit.Provide(Probe),
		fx.Invoke(func(API) {}),
	); err != nil {
		t.Fatalf("the fx example from docs/usage.md does not build: %v", err)
	}
}

func TestProbe(t *testing.T) {
	var path string
	var status atomic.Int64
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if code := int(status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"claude-opus-5","display_name":"Claude Opus 5","type":"model"}`))
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL, nil, nil)
	probe := Probe(c)
	if probe.Name != probeName {
		t.Fatalf("probe name = %q", probe.Name)
	}
	if err := probe.Run(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if path != "/v1/models/claude-opus-5" {
		t.Errorf("probe path = %q, want the default model lookup", path)
	}

	status.Store(http.StatusUnauthorized)
	if err := probe.Run(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("probe err = %v, want ErrAuth", err)
	}
}

func TestWithPriceTable(t *testing.T) {
	reader, _ := installOTel(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	// Наценка ×2 к тарифу claude-opus-5.
	marked := map[string]Price{"claude-opus-5": tier(10*usd, 50*usd, usd)}
	c, err := New(Config{
		APIKey: "sk-test", BaseURL: srv.URL, DefaultModel: "claude-opus-5",
		MaxRetries: 0, Timeout: 30 * time.Second,
	}, nil, WithPriceTable(marked))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := c.Complete(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !resp.CostKnown || resp.Cost != marked["claude-opus-5"].cost(resp.Usage) {
		t.Fatalf("cost = %d, want the custom table rate", resp.Cost)
	}
	if got := counterSum(t, reader, "anthropic_api_cost_micros_total", nil); got != int64(resp.Cost) {
		t.Errorf("cost_micros_total = %d, want %d", got, resp.Cost)
	}
	if cost, _ := Cost("claude-opus-5", resp.Usage); cost == resp.Cost {
		t.Error("the package-level Cost must keep using defaultPrices")
	}
}

func TestWithHTTPClient_TransportIsStillTraced(t *testing.T) {
	_, spans := installOTel(t)
	var custom atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(messageJSON))
	}))
	t.Cleanup(srv.Close)

	base := &http.Client{Transport: countingTransport{n: &custom, next: http.DefaultTransport}}
	c, err := New(Config{
		APIKey: "sk-test", BaseURL: srv.URL, DefaultModel: "claude-opus-5",
		MaxRetries: 0, Timeout: 30 * time.Second,
	}, nil, WithHTTPClient(base))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Complete(context.Background(), userRequest("hi")); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if custom.Load() != 1 {
		t.Errorf("custom transport used %d times, want 1", custom.Load())
	}
	var transport int
	for _, s := range spans.Ended() {
		if s.Name() != "anthropic.messages" {
			transport++
		}
	}
	if transport != 1 {
		t.Errorf("otelhttp spans = %d, want 1 — a custom transport must stay wrapped", transport)
	}
}

type countingTransport struct {
	n    *atomic.Int64
	next http.RoundTripper
}

func (t countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.n.Add(1)
	return t.next.RoundTrip(r)
}
