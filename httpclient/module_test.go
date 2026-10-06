package httpclient

import (
	"context"
	"errors"
	"strings"
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

// Breaker читается с тем же префиксом, что и плоские поля; имён с приставкой поля Breaker нет.
func TestNewConfigPrefixed_BreakerSharesPrefix(t *testing.T) {
	t.Setenv("AMOCRM_HTTPCLIENT_BREAKER_FAILURE_THRESHOLD", "1")
	t.Setenv("AMOCRM_HTTPCLIENT_BREAKER_RECOVERY_TIMEOUT", "2s")
	t.Setenv("BREAKER_HTTPCLIENT_BREAKER_FAILURE_THRESHOLD", "7")

	cfg, err := NewConfigPrefixed("AMOCRM")
	if err != nil {
		t.Fatalf("NewConfigPrefixed: %v", err)
	}
	if cfg.Breaker.FailureThreshold != 1 || cfg.Breaker.RecoveryTimeout != 2*time.Second {
		t.Fatalf("Breaker = %+v, want AMOCRM_HTTPCLIENT_BREAKER_*", cfg.Breaker)
	}
	base, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if base.Breaker.FailureThreshold != 5 {
		t.Fatalf("FailureThreshold = %d, want the default: BREAKER_HTTPCLIENT_BREAKER_* is no name", base.Breaker.FailureThreshold)
	}
}

func TestModule_ProvidesClientAndClosesOnStop(t *testing.T) {
	var (
		client *Client
		closed atomic.Bool
	)
	app := fxtest.New(t,
		fx.Supply(validConfig()),
		Module,
		fx.Supply(Options{WithTransport(closableTransport{closed: &closed})}),
		fx.Populate(&client),
	)
	app.RequireStart()
	if client == nil {
		t.Fatal("client not provided")
	}
	if _, ok := client.base.(closableTransport); !ok {
		t.Fatalf("option from Options was not applied: base is %T", client.base)
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

// Опции из графа — одно значение в заданном порядке (KX50): из двух hook'ов не выживает
// случайный, второе значение Options — ошибка графа, а не молчаливая потеря.
func TestModule_OptionsApplyInOrder(t *testing.T) {
	var order []int
	hook := func(n int) Option {
		return WithBeforeAttempt(func(context.Context, int) error { order = append(order, n); return nil })
	}
	var client *Client
	app := fxtest.New(t, fx.Supply(validConfig()), Module, fx.Supply(Options{hook(1), hook(2)}), fx.Populate(&client))
	app.RequireStart()
	defer app.RequireStop()
	for _, before := range client.before {
		if err := before(context.Background(), 0); err != nil {
			t.Fatalf("hook: %v", err)
		}
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("hooks ran %v, want [1 2]", order)
	}

	err := fx.New(fx.NopLogger, fx.Supply(validConfig()), Module,
		fx.Supply(Options{hook(1)}), fx.Supply(Options{hook(2)}), fx.Invoke(func(*Client) {})).Err()
	if err == nil || !strings.Contains(err.Error(), "already provided") {
		t.Fatalf("second Options value = %v, want an already provided graph error", err)
	}
}
