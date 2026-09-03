package natskit_test

import (
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/natskit"
)

func TestNewConfig_ReadsEnvAndDefaults(t *testing.T) {
	t.Setenv("NATS_URL", "nats://local:4222")
	t.Setenv("NATS_JETSTREAM_ENABLED", "true")
	t.Setenv("NATS_JS_STREAM_NAMES", "meeting, recording")
	t.Setenv("NATS_JS_NAK_BACKOFF", "5s")
	t.Setenv("NATS_MAX_RECONNECTS", "7")

	cfg, err := natskit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	if cfg.URL != "nats://local:4222" {
		t.Fatalf("URL = %q", cfg.URL)
	}
	if !cfg.JetStream.Enabled {
		t.Fatal("JetStream.Enabled = false")
	}
	if got := cfg.JetStream.StreamNames; len(got) != 2 || got[0] != "meeting" || got[1] != "recording" {
		t.Fatalf("StreamNames = %#v", got)
	}
	if cfg.JetStream.NakBackoff != 5*time.Second {
		t.Fatalf("NakBackoff = %v", cfg.JetStream.NakBackoff)
	}
	if cfg.MaxReconnects != 7 {
		t.Fatalf("MaxReconnects = %d", cfg.MaxReconnects)
	}
	if cfg.JetStream.MaxAge != 168*time.Hour {
		t.Fatalf("MaxAge default = %v", cfg.JetStream.MaxAge)
	}
	if cfg.Backpressure.MaxInFlight != 100 {
		t.Fatalf("MaxInFlight default = %d", cfg.Backpressure.MaxInFlight)
	}
	if cfg.DrainTimeout != 10*time.Second {
		t.Fatalf("DrainTimeout default = %v", cfg.DrainTimeout)
	}
	if !cfg.JetStream.StreamManaged {
		t.Fatal("StreamManaged default = false, want true")
	}
	if cfg.JetStream.PublishAsyncTimeout != 5*time.Second {
		t.Fatalf("PublishAsyncTimeout default = %v", cfg.JetStream.PublishAsyncTimeout)
	}
	if cfg.JetStream.HandlerTimeout != 0 {
		t.Fatalf("HandlerTimeout default = %v, want 0 (= AckWait)", cfg.JetStream.HandlerTimeout)
	}
}

func TestNewConfig_StreamManagedOff(t *testing.T) {
	t.Setenv("NATS_JS_STREAM_MANAGED", "false")

	cfg, err := natskit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.JetStream.StreamManaged {
		t.Fatal("StreamManaged = true, want false")
	}
}

// Инстанс с префиксом не наследует переменные дефолтного: иначе вторая шина
// молча ушла бы на чужой URL и попыталась создать там чужие стримы.
func TestNewConfigPrefixed_IgnoresUnprefixed(t *testing.T) {
	t.Setenv("NATS_URL", "nats://local:4222")
	t.Setenv("NATS_JETSTREAM_ENABLED", "true")
	t.Setenv("NATS_JS_STREAM_NAMES", "meeting,recording")
	t.Setenv("HUB_NATS_URL", "tls://hub.example:4222")
	t.Setenv("HUB_NATS_JETSTREAM_ENABLED", "true")

	cfg, err := natskit.NewConfigPrefixed("HUB")
	if err != nil {
		t.Fatalf("NewConfigPrefixed: %v", err)
	}

	if cfg.URL != "tls://hub.example:4222" {
		t.Fatalf("URL = %q", cfg.URL)
	}
	if len(cfg.JetStream.StreamNames) != 0 {
		t.Fatalf("StreamNames leaked from unprefixed env: %#v", cfg.JetStream.StreamNames)
	}
}

func TestNewConfigPrefixed_FallsBackToDefaults(t *testing.T) {
	t.Setenv("NATS_URL", "nats://local:4222")

	cfg, err := natskit.NewConfigPrefixed("HUB")
	if err != nil {
		t.Fatalf("NewConfigPrefixed: %v", err)
	}

	if cfg.URL != "nats://localhost:4222" {
		t.Fatalf("URL = %q, want tag default", cfg.URL)
	}
}

func TestNewConfig_RejectsMalformedValue(t *testing.T) {
	t.Setenv("NATS_JS_ACK_WAIT", "30")

	if _, err := natskit.NewConfig(); err == nil {
		t.Fatal("want error on duration without unit")
	}
}
