package httpclient

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestNewConfig_ReadsEnv(t *testing.T) {
	t.Setenv("HTTPCLIENT_TIMEOUT", "3s")
	t.Setenv("HTTPCLIENT_MAX_RETRIES", "1")
	t.Setenv("HTTPCLIENT_SSE_MAX_EVENT_BYTES", "2048")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Timeout != 3*time.Second || cfg.MaxRetries != 1 || cfg.SSEMaxEventBytes != 2048 {
		t.Fatalf("config: %+v", cfg)
	}
}

func TestNewConfig_RejectsInvalid(t *testing.T) {
	t.Setenv("HTTPCLIENT_MAX_RETRIES", "-1")
	if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestNewConfig_RejectsUnparsableEnv(t *testing.T) {
	t.Setenv("HTTPCLIENT_TIMEOUT", "soon")
	if _, err := NewConfig(); err == nil {
		t.Fatal("want error on unparsable HTTPCLIENT_TIMEOUT")
	}
}

func TestNewConfigPrefixed(t *testing.T) {
	t.Setenv("HTTPCLIENT_TIMEOUT", "9s")
	t.Setenv("HTTPCLIENT_MAX_RETRIES", "5")
	t.Setenv("AMOCRM_HTTPCLIENT_TIMEOUT", "1s")

	cfg, err := NewConfigPrefixed("AMOCRM")
	if err != nil {
		t.Fatalf("NewConfigPrefixed: %v", err)
	}
	if cfg.Timeout != time.Second {
		t.Errorf("prefixed value must win: got %v", cfg.Timeout)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("unprefixed value is the baseline: got %d", cfg.MaxRetries)
	}
}

func TestModule_ProvidesClientAndClosesOnStop(t *testing.T) {
	var (
		client *Client
		closed atomic.Bool
	)
	app := fxtest.New(t,
		fx.Supply(Config{Timeout: time.Second}),
		Module,
		ProvideOption(func() Option { return WithTransport(closableTransport{closed: &closed}) }),
		fx.Populate(&client),
	)
	app.RequireStart()
	if client == nil {
		t.Fatal("client not provided")
	}
	if _, ok := client.base.(closableTransport); !ok {
		t.Fatalf("option from the group was not applied: base is %T", client.base)
	}
	app.RequireStop()
	if !closed.Load() {
		t.Error("OnStop must close idle connections")
	}
}

func TestModule_InvalidConfigFailsGraph(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Supply(Config{MaxRetries: -1}),
		Module,
		fx.Invoke(func(*Client) {}),
	)
	if err := app.Err(); !errors.Is(err, ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}
