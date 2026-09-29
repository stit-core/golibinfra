package bruteforcekit_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/bruteforcekit"
)

func TestNewConfig_Defaults(t *testing.T) {
	for _, env := range []string{
		"BRUTEFORCE_MAX_FAILURES",
		"BRUTEFORCE_LOCK_DURATION",
		"BRUTEFORCE_CLEANUP_RETENTION",
		"BRUTEFORCE_CLEANUP_INTERVAL",
		"BRUTEFORCE_IPV6_PREFIX",
	} {
		// t.Setenv возвращает прежнее значение в Cleanup; Unsetenv убирает
		// переменную, чтобы envconfig взял default.
		t.Setenv(env, "")
		if err := os.Unsetenv(env); err != nil {
			t.Fatalf("unsetenv %s: %v", env, err)
		}
	}

	cfg, err := bruteforcekit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	want := bruteforcekit.Config{
		MaxFailures:      5,
		LockDuration:     15 * time.Minute,
		CleanupRetention: 24 * time.Hour,
		CleanupInterval:  5 * time.Minute,
		IPv6Prefix:       64,
	}
	if cfg != want {
		t.Fatalf("NewConfig = %+v, want %+v", cfg, want)
	}
}

func TestNewConfig_FromEnv(t *testing.T) {
	t.Setenv("BRUTEFORCE_MAX_FAILURES", "7")
	t.Setenv("BRUTEFORCE_LOCK_DURATION", "90s")
	t.Setenv("BRUTEFORCE_CLEANUP_RETENTION", "0s")
	t.Setenv("BRUTEFORCE_CLEANUP_INTERVAL", "1m")
	t.Setenv("BRUTEFORCE_IPV6_PREFIX", "128")

	cfg, err := bruteforcekit.NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	want := bruteforcekit.Config{
		MaxFailures:      7,
		LockDuration:     90 * time.Second,
		CleanupRetention: 0,
		CleanupInterval:  time.Minute,
		IPv6Prefix:       bruteforcekit.FullIPv6Prefix,
	}
	if cfg != want {
		t.Fatalf("NewConfig = %+v, want %+v", cfg, want)
	}
}

func TestNewConfig_InvalidEnv(t *testing.T) {
	t.Setenv("BRUTEFORCE_MAX_FAILURES", "0")
	if _, err := bruteforcekit.NewConfig(); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewConfig = %v, want ErrConfig", err)
	}

	t.Setenv("BRUTEFORCE_MAX_FAILURES", "not-a-number")
	if _, err := bruteforcekit.NewConfig(); !errors.Is(err, bruteforcekit.ErrConfig) {
		t.Fatalf("NewConfig with unparsable value = %v, want ErrConfig", err)
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()
	valid := bruteforcekit.Config{
		MaxFailures:      1,
		LockDuration:     time.Second,
		CleanupRetention: 0,
		CleanupInterval:  time.Minute,
		IPv6Prefix:       bruteforcekit.FullIPv6Prefix,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := map[string]func(c *bruteforcekit.Config){
		"zero value":        func(c *bruteforcekit.Config) { *c = bruteforcekit.Config{} },
		"no failures":       func(c *bruteforcekit.Config) { c.MaxFailures = 0 },
		"no lock":           func(c *bruteforcekit.Config) { c.LockDuration = 0 },
		"negative lock":     func(c *bruteforcekit.Config) { c.LockDuration = -time.Second },
		"negative reten":    func(c *bruteforcekit.Config) { c.CleanupRetention = -time.Second },
		"no interval":       func(c *bruteforcekit.Config) { c.CleanupInterval = 0 },
		"prefix too small":  func(c *bruteforcekit.Config) { c.IPv6Prefix = 0 },
		"prefix too large":  func(c *bruteforcekit.Config) { c.IPv6Prefix = 129 },
		"prefix negative":   func(c *bruteforcekit.Config) { c.IPv6Prefix = -1 },
		"interval negative": func(c *bruteforcekit.Config) { c.CleanupInterval = -time.Minute },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := valid
			mutate(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, bruteforcekit.ErrConfig) {
				t.Fatalf("Validate(%+v) = %v, want ErrConfig", cfg, err)
			}
			if !strings.HasPrefix(err.Error(), "bruteforcekit: ") {
				t.Fatalf("Validate error = %q, want bruteforcekit: prefix", err)
			}
		})
	}
}
