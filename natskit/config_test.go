package natskit_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		t.Fatalf("JetStream.HandlerTimeout default = %v, want 0 (= AckWait)", cfg.JetStream.HandlerTimeout)
	}
}

// Дефолты poison-обработки и reconnect'ов: без них одно битое сообщение крутится
// в горячей петле, а двухминутная недоступность NATS убивает шину навсегда.
func TestNewConfig_SafeDefaults(t *testing.T) {
	cfg, err := natskit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.MaxReconnects != -1 {
		t.Errorf("MaxReconnects default = %d, want -1 (infinite)", cfg.MaxReconnects)
	}
	if cfg.HandlerTimeout != 30*time.Second {
		t.Errorf("HandlerTimeout default = %v, want 30s", cfg.HandlerTimeout)
	}
	if cfg.JetStream.MaxDeliver != 21 {
		t.Errorf("MaxDeliver default = %d, want 21", cfg.JetStream.MaxDeliver)
	}
	if cfg.JetStream.NakBackoff != time.Second {
		t.Errorf("NakBackoff default = %v, want 1s", cfg.JetStream.NakBackoff)
	}
	if cfg.JetStream.NakMaxBackoff != time.Minute {
		t.Errorf("NakMaxBackoff default = %v, want 1m", cfg.JetStream.NakMaxBackoff)
	}
	if cfg.JetStream.AckWait != 30*time.Second {
		t.Errorf("AckWait default = %v, want 30s", cfg.JetStream.AckWait)
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
func TestNewInstanceConfig_IgnoresUnprefixed(t *testing.T) {
	t.Setenv("NATS_URL", "nats://local:4222")
	t.Setenv("NATS_JETSTREAM_ENABLED", "true")
	t.Setenv("NATS_JS_STREAM_NAMES", "meeting,recording")
	t.Setenv("HUB_NATS_URL", "tls://hub.example:4222")
	t.Setenv("HUB_NATS_JETSTREAM_ENABLED", "true")

	cfg, err := natskit.NewInstanceConfig("hub")
	if err != nil {
		t.Fatalf("NewInstanceConfig: %v", err)
	}

	if cfg.URL != "tls://hub.example:4222" {
		t.Fatalf("URL = %q", cfg.URL)
	}
	if len(cfg.JetStream.StreamNames) != 0 {
		t.Fatalf("StreamNames leaked from unprefixed env: %#v", cfg.JetStream.StreamNames)
	}
	if cfg.Instance != "hub" {
		t.Fatalf("Instance = %q, want hub", cfg.Instance)
	}
}

func TestNewInstanceConfig_FallsBackToDefaults(t *testing.T) {
	t.Setenv("NATS_URL", "nats://local:4222")

	cfg, err := natskit.NewInstanceConfig("HUB")
	if err != nil {
		t.Fatalf("NewInstanceConfig: %v", err)
	}

	if cfg.URL != "nats://localhost:4222" {
		t.Fatalf("URL = %q, want tag default", cfg.URL)
	}
}

// Имя инстанса, из которого нельзя собрать префикс ENV, — ошибка конфига, а не
// молчаливый инстанс на дефолтах.
func TestNewInstanceConfig_RejectsInvalidName(t *testing.T) {
	if _, err := natskit.NewInstanceConfig("hub/v2"); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewInstanceConfig(\"hub/v2\") err = %v, want ErrConfig", err)
	}
}

func TestNewConfig_RejectsMalformedValue(t *testing.T) {
	t.Setenv("NATS_JS_ACK_WAIT", "30")

	if _, err := natskit.NewConfig(); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewConfig err = %v, want ErrConfig on duration without unit", err)
	}
}

func TestNewConfig_RejectsInvalidValue(t *testing.T) {
	t.Setenv("NATS_URL", "   ")

	if _, err := natskit.NewConfig(); !errors.Is(err, natskit.ErrConfig) {
		t.Fatalf("NewConfig err = %v, want ErrConfig on blank NATS_URL", err)
	}
}

// Заданная, но пустая переменная (`.env` со строкой `NATS_JS_NAK_BACKOFF=`,
// Helm с `value: ""`, compose с незаданной подстановкой) равна незаданной:
// она не должна обнулять безопасные дефолты кита.
func TestNewConfig_EmptyEnvFallsBackToDefaults(t *testing.T) {
	defaults, err := natskit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	keys := []string{
		"NATS_URL",
		"NATS_CONNECT_TIMEOUT",
		"NATS_RECONNECT_WAIT",
		"NATS_MAX_RECONNECTS",
		"NATS_DRAIN_TIMEOUT",
		"NATS_HANDLER_TIMEOUT",
		"NATS_MAX_IN_FLIGHT",
		"NATS_JETSTREAM_ENABLED",
		"NATS_JS_STREAM_MANAGED",
		"NATS_JS_RETENTION",
		"NATS_JS_MAX_AGE",
		"NATS_JS_DUPLICATES",
		"NATS_JS_MAX_IN_FLIGHT",
		"NATS_JS_ACK_WAIT",
		"NATS_JS_HANDLER_TIMEOUT",
		"NATS_JS_PUBLISH_ASYNC_TIMEOUT",
		"NATS_JS_MAX_DELIVER",
		"NATS_JS_NAK_BACKOFF",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			got, err := natskit.NewConfig()
			if err != nil {
				t.Fatalf("NewConfig with %s empty: %v", key, err)
			}
			if !reflect.DeepEqual(got, defaults) {
				t.Fatalf("%s empty changed the config:\n got %+v\nwant %+v", key, got, defaults)
			}
		})
	}
}

func validConfig() natskit.Config {
	return natskit.Config{
		URL:            "nats://localhost:4222",
		ConnectTimeout: 5 * time.Second,
		ReconnectWait:  2 * time.Second,
		MaxReconnects:  -1,
		DrainTimeout:   10 * time.Second,
		HandlerTimeout: 30 * time.Second,
		Backpressure:   natskit.BackpressureConfig{MaxInFlight: 100},
		JetStream: natskit.JetStreamConfig{
			Enabled:       true,
			StreamNames:   []string{"ORDERS"},
			StreamManaged: true,
			Retention:     "limits",
			MaxAge:        time.Hour,
			Duplicates:    time.Minute,
			MaxInFlight:   100,
			AckWait:       30 * time.Second,
			MaxDeliver:    10,
			NakBackoff:    time.Second,
			DLQSubject:    "DLQ.orders",

			PublishAsyncTimeout: 5 * time.Second,
		},
	}
}

func TestConfig_Validate(t *testing.T) {
	existingFile := filepath.Join(t.TempDir(), "creds")
	if err := os.WriteFile(existingFile, []byte("creds"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*natskit.Config)
		wantErr bool
	}{
		{"valid", func(*natskit.Config) {}, false},
		{"zero handler timeouts and nak cap are values", func(c *natskit.Config) {
			c.HandlerTimeout, c.JetStream.HandlerTimeout, c.JetStream.NakMaxBackoff = 0, 0, 0
		}, false},
		{"jetstream off needs no values", func(c *natskit.Config) { c.JetStream = natskit.JetStreamConfig{} }, false},
		{"url list", func(c *natskit.Config) { c.URL = "nats://a:4222,nats://b:4222" }, false},
		{"url without scheme", func(c *natskit.Config) { c.URL = "localhost:4222" }, false},
		{"escaped at in websocket path", func(c *natskit.Config) { c.URL = "wss://nats.example.com/u/me%40x.com" }, false},
		{"bare at in websocket path", func(c *natskit.Config) { c.URL = "wss://nats.example.com/u/me@x.com" }, true},
		{"creds file present", func(c *natskit.Config) { c.CredsFile = existingFile }, false},
		{"instance name", func(c *natskit.Config) { c.Instance = "hub-2.eu" }, false},

		{"zero connect timeout", func(c *natskit.Config) { c.ConnectTimeout = 0 }, true},
		{"zero reconnect wait", func(c *natskit.Config) { c.ReconnectWait = 0 }, true},
		{"zero drain timeout", func(c *natskit.Config) { c.DrainTimeout = 0 }, true},
		{"zero max in flight", func(c *natskit.Config) { c.Backpressure.MaxInFlight = 0 }, true},
		{"zero js ack wait", func(c *natskit.Config) { c.JetStream.AckWait = 0 }, true},
		{"zero js max age", func(c *natskit.Config) { c.JetStream.MaxAge = 0 }, true},
		{"zero js duplicates", func(c *natskit.Config) { c.JetStream.Duplicates = 0 }, true},
		{"zero js publish timeout", func(c *natskit.Config) { c.JetStream.PublishAsyncTimeout = 0 }, true},
		{"zero js max in flight", func(c *natskit.Config) { c.JetStream.MaxInFlight = 0 }, true},
		{"jetstream off still checks the format", func(c *natskit.Config) {
			c.JetStream = natskit.JetStreamConfig{AckWait: -time.Second}
		}, true},
		{"jetstream off still checks retention", func(c *natskit.Config) {
			c.JetStream = natskit.JetStreamConfig{Retention: "nonsense"}
		}, true},
		{"empty url", func(c *natskit.Config) { c.URL = "" }, true},
		{"url scheme", func(c *natskit.Config) { c.URL = "http://localhost:4222" }, true},
		{"url without host", func(c *natskit.Config) { c.URL = "nats://" }, true},
		{"url list with a hole", func(c *natskit.Config) { c.URL = "nats://a:4222,," }, true},
		{"negative connect timeout", func(c *natskit.Config) { c.ConnectTimeout = -time.Second }, true},
		{"negative drain timeout", func(c *natskit.Config) { c.DrainTimeout = -time.Second }, true},
		{"negative handler timeout", func(c *natskit.Config) { c.HandlerTimeout = -time.Second }, true},
		{"max reconnects below -1", func(c *natskit.Config) { c.MaxReconnects = -2 }, true},
		{"negative max in flight", func(c *natskit.Config) { c.Backpressure.MaxInFlight = -1 }, true},
		{"instance name with slash", func(c *natskit.Config) { c.Instance = "hub/v2" }, true},
		{"missing creds file", func(c *natskit.Config) { c.CredsFile = "/nope/creds" }, true},
		{"missing nkey seed", func(c *natskit.Config) { c.NKeySeedFile = "/nope/seed" }, true},
		{"missing tls ca", func(c *natskit.Config) { c.TLSCAFile = "/nope/ca.pem" }, true},
		{"ack wait below a second", func(c *natskit.Config) { c.JetStream.AckWait = time.Millisecond }, true},
		{"negative ack wait", func(c *natskit.Config) { c.JetStream.AckWait = -time.Second }, true},
		{"negative nak backoff", func(c *natskit.Config) { c.JetStream.NakBackoff = -time.Second }, true},
		{"negative nak max backoff", func(c *natskit.Config) { c.JetStream.NakMaxBackoff = -time.Second }, true},
		{"negative max deliver", func(c *natskit.Config) { c.JetStream.MaxDeliver = -1 }, true},
		{"unknown retention", func(c *natskit.Config) { c.JetStream.Retention = "nonsense" }, true},
		{"stream name with a dot", func(c *natskit.Config) { c.JetStream.StreamNames = []string{"a.b"} }, true},
		{"dlq subject with a wildcard", func(c *natskit.Config) { c.JetStream.DLQSubject = "dlq.>" }, true},
		{"dlq subject with a space", func(c *natskit.Config) { c.JetStream.DLQSubject = "dlq orders" }, true},
		{"dlq subject with an empty token", func(c *natskit.Config) { c.JetStream.DLQSubject = "dlq..orders" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr && !errors.Is(err, natskit.ErrConfig) {
				t.Fatalf("Validate = %v, want ErrConfig", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
		})
	}
}
