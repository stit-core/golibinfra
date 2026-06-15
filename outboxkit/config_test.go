package outboxkit_test

import (
	"testing"

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
	}
}

func TestNewConfig_InvalidMode(t *testing.T) {
	t.Setenv("OUTBOX_PUBLISH_MODE", "bogus")
	if _, err := outboxkit.NewConfig(); err == nil {
		t.Fatal("expected error for invalid OUTBOX_PUBLISH_MODE")
	}
}
