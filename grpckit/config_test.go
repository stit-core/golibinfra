package grpckit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"zero value is valid", Config{}, false},
		{"env defaults", Config{Addr: ":9090", MaxRecvMsgSize: 4 << 20, MaxSendMsgSize: 4 << 20, ShutdownGrace: 10 * time.Second, HealthInterval: 10 * time.Second}, false},
		{"negative recv limit", Config{MaxRecvMsgSize: -1}, true},
		{"negative send limit", Config{MaxSendMsgSize: -1}, true},
		{"negative shutdown grace", Config{ShutdownGrace: -time.Second}, true},
		{"negative health interval", Config{HealthInterval: -time.Second}, true},
		{"negative health probe timeout", Config{HealthProbeTimeout: -time.Second}, true},
		{"zero health probe timeout falls back to default", Config{HealthProbeTimeout: 0}, false},
		{"negative keepalive time", Config{Keepalive: KeepaliveConfig{Time: -time.Second}}, true},
		{"negative keepalive timeout", Config{Keepalive: KeepaliveConfig{Timeout: -time.Second}}, true},
		{"negative keepalive min time", Config{Keepalive: KeepaliveConfig{MinTime: -time.Second}}, true},
		{"cert without key", Config{TLSCertFile: "cert.pem"}, true},
		{"key without cert", Config{TLSKeyFile: "key.pem"}, true},
		{"cert and key", Config{TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}, false},
		{"zero health interval disables probes", Config{HealthInterval: 0}, false},
		{"zero max concurrent streams is unlimited", Config{MaxConcurrentStreams: 0}, false},
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
		cfg, err := NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := Config{
			Addr:               defaultAddr,
			MaxRecvMsgSize:     defaultMaxMsgSize,
			MaxSendMsgSize:     defaultMaxMsgSize,
			ShutdownGrace:      defaultShutdownGrace,
			HealthInterval:     10 * time.Second,
			HealthProbeTimeout: defaultHealthProbeTimeout,
			Keepalive: KeepaliveConfig{
				Time:    defaultKeepaliveTime,
				Timeout: defaultKeepaliveTmout,
				MinTime: defaultKeepaliveMin,
			},
		}
		if cfg != want {
			t.Fatalf("NewConfig() = %+v, want %+v", cfg, want)
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("GRPC_ADDR", "127.0.0.1:19090")
		t.Setenv("GRPC_MAX_RECV_MSG_SIZE", "1048576")
		t.Setenv("GRPC_MAX_CONCURRENT_STREAMS", "256")
		t.Setenv("GRPC_KEEPALIVE_MIN_TIME", "30s")
		cfg, err := NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.Addr != "127.0.0.1:19090" || cfg.MaxRecvMsgSize != 1<<20 || cfg.MaxConcurrentStreams != 256 || cfg.Keepalive.MinTime != 30*time.Second {
			t.Fatalf("NewConfig() = %+v", cfg)
		}
	})

	t.Run("invalid env fails fast", func(t *testing.T) {
		t.Setenv("GRPC_TLS_CERT_FILE", "/tmp/cert.pem")
		if _, err := NewConfig(); !errors.Is(err, ErrConfig) {
			t.Fatalf("NewConfig() = %v, want ErrConfig", err)
		}
	})

	t.Run("unparsable env fails", func(t *testing.T) {
		t.Setenv("GRPC_SHUTDOWN_GRACE", "not-a-duration")
		if _, err := NewConfig(); err == nil {
			t.Fatal("NewConfig() = nil, want error")
		}
	})
}

// Дефолты сервера обязаны быть не строже клиентских ping'ов grpcclient, иначе
// сервер шлёт GOAWAY too_many_pings связке «кит + кит из коробки».
func TestKeepaliveDefaultIsClientCompatible(t *testing.T) {
	const clientKeepaliveTime = 30 * time.Second
	if defaultKeepaliveMin > clientKeepaliveTime {
		t.Fatalf("GRPC_KEEPALIVE_MIN_TIME default %s > grpcclient GRPC_CLIENT_KEEPALIVE_TIME default %s", defaultKeepaliveMin, clientKeepaliveTime)
	}
}

func TestNewRejectsInvalidConfigAndTLSFiles(t *testing.T) {
	if _, err := New(Config{TLSCertFile: "cert.pem"}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("New with half TLS pair = %v, want ErrConfig", err)
	}

	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{TLSCertFile: cert, TLSKeyFile: key}, nil)
	if err == nil {
		t.Fatal("New with broken cert/key = nil, want error")
	}
}
