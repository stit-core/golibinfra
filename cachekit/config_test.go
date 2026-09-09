package cachekit_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/cachekit"
)

func validConfig() cachekit.Config {
	return cachekit.Config{
		URL:          "redis://localhost:6379/0",
		PoolSize:     10,
		MaxRetries:   3,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cachekit.Config)
		ok     bool
	}{
		{name: "defaults", mutate: func(*cachekit.Config) {}, ok: true},
		{name: "prefix and no retries", mutate: func(c *cachekit.Config) { c.KeyPrefix, c.MaxRetries = "svc:", -1 }, ok: true},
		{name: "zero timeouts", mutate: func(c *cachekit.Config) { c.DialTimeout, c.ReadTimeout, c.WriteTimeout = 0, 0, 0 }, ok: true},
		{name: "rediss url", mutate: func(c *cachekit.Config) { c.URL = "rediss://user:pass@redis:6379/3" }, ok: true},
		{name: "pool size zero", mutate: func(c *cachekit.Config) { c.PoolSize = 0 }},
		{name: "pool size negative", mutate: func(c *cachekit.Config) { c.PoolSize = -1 }},
		{name: "max retries below -1", mutate: func(c *cachekit.Config) { c.MaxRetries = -2 }},
		{name: "negative dial timeout", mutate: func(c *cachekit.Config) { c.DialTimeout = -time.Second }},
		{name: "negative read timeout", mutate: func(c *cachekit.Config) { c.ReadTimeout = -time.Second }},
		{name: "negative write timeout", mutate: func(c *cachekit.Config) { c.WriteTimeout = -time.Second }},
		{name: "empty url", mutate: func(c *cachekit.Config) { c.URL = "" }},
		{name: "unsupported scheme", mutate: func(c *cachekit.Config) { c.URL = "http://localhost:6379" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			switch {
			case tt.ok && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case !tt.ok && !errors.Is(err, cachekit.ErrConfig):
				t.Fatalf("Validate() = %v, want ErrConfig", err)
			}
			cache, err := cachekit.NewRedisCache(cfg)
			switch {
			case tt.ok && err != nil:
				t.Fatalf("NewRedisCache() = %v, want nil", err)
			case tt.ok:
				_ = cache.Close()
			case !errors.Is(err, cachekit.ErrConfig):
				t.Fatalf("NewRedisCache() = %v, want ErrConfig", err)
			}
		})
	}
}

func TestNewConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		setRedisEnv(t, map[string]string{})

		cfg, err := cachekit.NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		want := cachekit.Config{
			URL: "redis://localhost:6379/0", PoolSize: 10, MaxRetries: 3,
			DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
			PingOnStart: true,
		}
		if cfg != want {
			t.Fatalf("NewConfig() = %+v, want %+v", cfg, want)
		}
	})

	t.Run("env", func(t *testing.T) {
		setRedisEnv(t, map[string]string{
			"REDIS_URL":           "redis://cache:6380/2",
			"REDIS_KEY_PREFIX":    "svc:",
			"REDIS_POOL_SIZE":     "25",
			"REDIS_MAX_RETRIES":   "-1",
			"REDIS_PING_ON_START": "false",
		})

		cfg, err := cachekit.NewConfig()
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.URL != "redis://cache:6380/2" || cfg.KeyPrefix != "svc:" || cfg.PoolSize != 25 ||
			cfg.MaxRetries != -1 || cfg.PingOnStart {
			t.Fatalf("NewConfig() = %+v", cfg)
		}
	})

	t.Run("invalid value fails fast", func(t *testing.T) {
		setRedisEnv(t, map[string]string{"REDIS_POOL_SIZE": "0"})

		if _, err := cachekit.NewConfig(); !errors.Is(err, cachekit.ErrConfig) {
			t.Fatalf("NewConfig() = %v, want ErrConfig", err)
		}
	})

	t.Run("unparsable value", func(t *testing.T) {
		setRedisEnv(t, map[string]string{"REDIS_READ_TIMEOUT": "soon"})

		if _, err := cachekit.NewConfig(); err == nil {
			t.Fatal("NewConfig() = nil, want error")
		}
	})
}

// setRedisEnv фиксирует всё окружение кита: иначе REDIS_* разработчика меняет
// результат теста дефолтов.
func setRedisEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, key := range []string{
		"REDIS_URL", "REDIS_KEY_PREFIX", "REDIS_POOL_SIZE", "REDIS_MAX_RETRIES",
		"REDIS_DIAL_TIMEOUT", "REDIS_READ_TIMEOUT", "REDIS_WRITE_TIMEOUT", "REDIS_PING_ON_START",
	} {
		if v, ok := env[key]; ok {
			t.Setenv(key, v)
			continue
		}
		// t.Setenv регистрирует восстановление прежнего значения; os.Unsetenv
		// поверх него убирает переменную, чтобы сработал default envconfig.
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetenv %s: %v", key, err)
		}
	}
}
