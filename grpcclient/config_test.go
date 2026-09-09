package grpcclient

import (
	"errors"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	const target = "dns:///billing:9090"
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"target only", Config{Target: target}, false},
		{"env defaults", Config{Target: target, Timeout: 10 * time.Second, MaxRecvMsgSize: 4 << 20, MaxSendMsgSize: 4 << 20, Keepalive: KeepaliveConfig{Time: 30 * time.Second, Timeout: 20 * time.Second}}, false},
		{"empty target", Config{}, true},
		{"negative recv limit", Config{Target: target, MaxRecvMsgSize: -1}, true},
		{"negative send limit", Config{Target: target, MaxSendMsgSize: -1}, true},
		{"negative timeout", Config{Target: target, Timeout: -time.Second}, true},
		{"zero timeout means ctx only", Config{Target: target, Timeout: 0}, false},
		{"keepalive below the grpc-go floor", Config{Target: target, Keepalive: KeepaliveConfig{Time: time.Second}}, true},
		{"keepalive at the floor", Config{Target: target, Keepalive: KeepaliveConfig{Time: minKeepaliveTime}}, false},
		{"negative keepalive timeout", Config{Target: target, Keepalive: KeepaliveConfig{Timeout: -time.Second}}, true},
		{"token over plaintext", Config{Target: target, Token: "t"}, true},
		{"token over plaintext allowed explicitly", Config{Target: target, Token: "t", AllowInsecureToken: true}, false},
		{"token over tls", Config{Target: target, Token: "t", TLS: true}, false},
		{"ca file without tls", Config{Target: target, TLSCAFile: "ca.pem"}, true},
		{"server name without tls", Config{Target: target, TLSServerName: "billing"}, true},
		{"ca file with tls", Config{Target: target, TLS: true, TLSCAFile: "ca.pem", TLSServerName: "billing"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if !errors.Is(err, ErrConfig) {
					t.Fatalf("Validate() = %v, want ErrConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestNewConfigReadsEnvAndValidates(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("GRPC_CLIENT_TARGET", "dns:///billing:9090")
		cfg, err := NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := Config{
			Target:         "dns:///billing:9090",
			Timeout:        10 * time.Second,
			MaxRecvMsgSize: defaultMaxMsgSize,
			MaxSendMsgSize: defaultMaxMsgSize,
			Keepalive: KeepaliveConfig{
				Time:    defaultKeepaliveTime,
				Timeout: defaultKeepaliveTmout,
			},
		}
		if cfg != want {
			t.Fatalf("NewConfig() = %+v, want %+v", cfg, want)
		}
	})

	t.Run("target is required", func(t *testing.T) {
		t.Setenv("GRPC_CLIENT_TARGET", "")
		if _, err := NewConfig(); err == nil {
			t.Fatal("NewConfig() without GRPC_CLIENT_TARGET = nil, want error")
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("GRPC_CLIENT_TARGET", "dns:///billing:9090")
		t.Setenv("GRPC_CLIENT_TIMEOUT", "0")
		t.Setenv("GRPC_CLIENT_MAX_RECV_MSG_SIZE", "1048576")
		t.Setenv("GRPC_CLIENT_TLS", "true")
		t.Setenv("GRPC_CLIENT_TLS_SERVER_NAME", "billing.internal")
		cfg, err := NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.Timeout != 0 || cfg.MaxRecvMsgSize != 1<<20 || !cfg.TLS || cfg.TLSServerName != "billing.internal" {
			t.Fatalf("NewConfig() = %+v", cfg)
		}
	})

	t.Run("unparsable env fails", func(t *testing.T) {
		t.Setenv("GRPC_CLIENT_TARGET", "dns:///billing:9090")
		t.Setenv("GRPC_CLIENT_TIMEOUT", "not-a-duration")
		if _, err := NewConfig(); err == nil {
			t.Fatal("NewConfig() = nil, want a read error")
		}
	})

	t.Run("insecure token fails fast", func(t *testing.T) {
		t.Setenv("GRPC_CLIENT_TARGET", "dns:///billing:9090")
		t.Setenv("GRPC_CLIENT_TOKEN", "secret")
		if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("NewConfig() = %v, want ErrConfig", err)
		}
		t.Setenv("GRPC_CLIENT_ALLOW_INSECURE_TOKEN", "true")
		if _, err := NewConfig(); err != nil {
			t.Fatalf("NewConfig() with an explicit opt-in: %v", err)
		}
	})
}

func TestWithDefaults(t *testing.T) {
	got := Config{Target: "x"}.withDefaults()
	if got.MaxRecvMsgSize != defaultMaxMsgSize || got.MaxSendMsgSize != defaultMaxMsgSize {
		t.Fatalf("message limits = %d/%d, want %d", got.MaxRecvMsgSize, got.MaxSendMsgSize, defaultMaxMsgSize)
	}
	if got.Keepalive.Time != defaultKeepaliveTime || got.Keepalive.Timeout != defaultKeepaliveTmout {
		t.Fatalf("keepalive = %+v", got.Keepalive)
	}
	if got.Timeout != 0 {
		t.Fatalf("Timeout = %s, want 0 (deadline from ctx only)", got.Timeout)
	}
}
