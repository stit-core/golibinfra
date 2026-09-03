package outboxkit_test

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/outboxkit"
)

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
		if cfg.TickTimeout != 30*time.Second {
			t.Fatalf("TickTimeout default = %v, want 30s", cfg.TickTimeout)
		}
	}
}

func TestNewConfig_InvalidMode(t *testing.T) {
	t.Setenv("OUTBOX_PUBLISH_MODE", "bogus")
	if _, err := outboxkit.NewConfig(); err == nil {
		t.Fatal("expected error for invalid OUTBOX_PUBLISH_MODE")
	}
}

func TestConfig_Validate(t *testing.T) {
	base := outboxkit.Config{BatchSize: 1, Interval: time.Second, PublishMode: outboxkit.PublishBatch}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := []outboxkit.Config{
		{BatchSize: 0, Interval: time.Second, PublishMode: outboxkit.PublishBatch},
		{BatchSize: 1, Interval: 0, PublishMode: outboxkit.PublishBatch},
		{BatchSize: 1, Interval: time.Second, PublishMode: outboxkit.PublishBatch, TickTimeout: -time.Second},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", c)
		}
	}
}
