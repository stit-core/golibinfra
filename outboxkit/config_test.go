package outboxkit_test

import (
	"errors"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

func TestNewConfig_Defaults(t *testing.T) {
	cfg, err := outboxkit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	want := outboxkit.Config{
		BatchSize:       100,
		Interval:        time.Second,
		PublishMode:     outboxkit.PublishBatch,
		MaxAttempts:     outboxkit.DefaultMaxAttempts,
		TickTimeout:     30 * time.Second,
		RetryBackoff:    time.Second,
		RetryMaxBackoff: 5 * time.Minute,
		MaxPayloadBytes: outboxkit.DefaultMaxPayloadBytes,
	}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
}

func TestNewConfig_ValidModes(t *testing.T) {
	for _, mode := range []string{"batch", "ordered"} {
		t.Setenv("OUTBOX_PUBLISH_MODE", mode)
		cfg, err := outboxkit.NewConfig()
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if string(cfg.PublishMode) != mode {
			t.Fatalf("PublishMode = %q, want %q", cfg.PublishMode, mode)
		}
	}
}

func TestNewConfig_InvalidValueIsErrConfig(t *testing.T) {
	t.Setenv("OUTBOX_PUBLISH_MODE", "bogus")
	_, err := outboxkit.NewConfig()
	if !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_UnparsableValue(t *testing.T) {
	t.Setenv("OUTBOX_BATCH_SIZE", "many")
	if _, err := outboxkit.NewConfig(); err == nil {
		t.Fatal("expected error for unparsable OUTBOX_BATCH_SIZE")
	}
}

func TestNewConfig_BackoffFromEnv(t *testing.T) {
	t.Setenv("OUTBOX_RETRY_BACKOFF", "2s")
	t.Setenv("OUTBOX_RETRY_MAX_BACKOFF", "1m")
	cfg, err := outboxkit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.RetryBackoff != 2*time.Second || cfg.RetryMaxBackoff != time.Minute {
		t.Fatalf("backoff = %v/%v, want 2s/1m", cfg.RetryBackoff, cfg.RetryMaxBackoff)
	}
}

func TestConfig_Validate(t *testing.T) {
	base := outboxkit.Config{
		BatchSize: 1, Interval: time.Second, PublishMode: outboxkit.PublishBatch, MaxAttempts: 1,
		TickTimeout: 30 * time.Second, RetryBackoff: time.Second, RetryMaxBackoff: time.Minute,
		MaxPayloadBytes: 1,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	unlimited := base
	unlimited.MaxAttempts = -1
	if err := unlimited.Validate(); err != nil {
		t.Fatalf("MaxAttempts=-1 (unlimited) rejected: %v", err)
	}

	bad := map[string]func(c outboxkit.Config) outboxkit.Config{
		"batch size": func(c outboxkit.Config) outboxkit.Config { c.BatchSize = 0; return c },
		"interval":   func(c outboxkit.Config) outboxkit.Config { c.Interval = 0; return c },
		"tick":       func(c outboxkit.Config) outboxkit.Config { c.TickTimeout = -time.Second; return c },
		// Тик идёт на WithoutCancel: «без дедлайна» — транзакция, которую не
		// обрывает ни остановка сервиса, ни таймер.
		"tick zero":   func(c outboxkit.Config) outboxkit.Config { c.TickTimeout = 0; return c },
		"mode":        func(c outboxkit.Config) outboxkit.Config { c.PublishMode = "sync"; return c },
		"backoff":     func(c outboxkit.Config) outboxkit.Config { c.RetryBackoff = -time.Second; return c },
		"max backoff": func(c outboxkit.Config) outboxkit.Config { c.RetryMaxBackoff = -time.Second; return c },
		"backoff cap": func(c outboxkit.Config) outboxkit.Config { c.RetryMaxBackoff = time.Millisecond; return c },
		// Строка без предела попыток держала бы ordered-очередь вечно.
		"max attempts":       func(c outboxkit.Config) outboxkit.Config { c.MaxAttempts = 0; return c },
		"max attempts below": func(c outboxkit.Config) outboxkit.Config { c.MaxAttempts = -2; return c },
		"max payload":        func(c outboxkit.Config) outboxkit.Config { c.MaxPayloadBytes = 0; return c },
	}
	for name, mutate := range bad {
		if err := mutate(base).Validate(); !errors.Is(err, outboxkit.ErrConfig) {
			t.Fatalf("%s: err = %v, want ErrConfig", name, err)
		}
	}
}

func TestNewConfig_ZeroTickTimeoutIsErrConfig(t *testing.T) {
	t.Setenv("OUTBOX_TICK_TIMEOUT", "0")
	_, err := outboxkit.NewConfig()
	if !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

// OUTBOX_MAX_ATTEMPTS=0 — ошибка старта; «без предела» — только явное -1.
func TestNewConfig_MaxAttemptsUnlimitedIsExplicit(t *testing.T) {
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "0")
	if _, err := outboxkit.NewConfig(); !errors.Is(err, outboxkit.ErrConfig) {
		t.Fatalf("OUTBOX_MAX_ATTEMPTS=0: err = %v, want ErrConfig", err)
	}
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "-1")
	if cfg, err := outboxkit.NewConfig(); err != nil || cfg.MaxAttempts != -1 {
		t.Fatalf("OUTBOX_MAX_ATTEMPTS=-1: cfg.MaxAttempts = %d, err = %v; want -1, nil", cfg.MaxAttempts, err)
	}
}
