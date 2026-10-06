package bruteforcekit

import (
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// ErrConfig — невалидный конфиг кита; возвращается Validate, NewConfig и
// конструкторами хранилищ.
var ErrConfig = errors.New("bruteforcekit: invalid config")

// FullIPv6Prefix — Config.IPv6Prefix, при котором IPv6-адреса не агрегируются:
// счётчик ведётся на полный адрес.
const FullIPv6Prefix = 128

// Config — политика guard'а. Имена полей и envconfig-теги — часть public API.
type Config struct {
	// MaxFailures — число неудач в окне, после которого пара блокируется.
	MaxFailures int `envconfig:"BRUTEFORCE_MAX_FAILURES" default:"5"`
	// LockDuration — длительность блокировки от момента достижения порога.
	LockDuration time.Duration `envconfig:"BRUTEFORCE_LOCK_DURATION" default:"15m"`
	// CleanupRetention — возраст разблокированной строки, после которого её
	// удаляет Cleanup.
	CleanupRetention time.Duration `envconfig:"BRUTEFORCE_CLEANUP_RETENTION" default:"24h"`
	// CleanupInterval — период job'а Cleanup, который регистрирует pgstore.Module.
	CleanupInterval time.Duration `envconfig:"BRUTEFORCE_CLEANUP_INTERVAL" default:"5m"`
	// IPv6Prefix — длина префикса, до которой агрегируются IPv6-адреса: 64 —
	// /64-подсеть, которую провайдер выдаёт клиенту целиком; FullIPv6Prefix —
	// без агрегации. На IPv4 не влияет.
	IPv6Prefix int `envconfig:"BRUTEFORCE_IPV6_PREFIX" default:"64"`
}

// NewConfig читает Config из ENV и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate проверяет инварианты конфига; нарушение — ErrConfig. Нулевой Config
// отвергается: guard, который молча никогда не блокирует, хуже упавшего старта.
func (c Config) Validate() error {
	switch {
	case c.MaxFailures < 1:
		return fmt.Errorf("%w: BRUTEFORCE_MAX_FAILURES must be >= 1, got %d", ErrConfig, c.MaxFailures)
	case c.LockDuration <= 0:
		return fmt.Errorf("%w: BRUTEFORCE_LOCK_DURATION must be > 0, got %s", ErrConfig, c.LockDuration)
	case c.CleanupRetention < 0:
		return fmt.Errorf("%w: BRUTEFORCE_CLEANUP_RETENTION must be >= 0, got %s", ErrConfig, c.CleanupRetention)
	case c.CleanupInterval <= 0:
		return fmt.Errorf("%w: BRUTEFORCE_CLEANUP_INTERVAL must be > 0, got %s", ErrConfig, c.CleanupInterval)
	case c.IPv6Prefix < 1 || c.IPv6Prefix > FullIPv6Prefix:
		return fmt.Errorf("%w: BRUTEFORCE_IPV6_PREFIX must be in [1, %d], got %d", ErrConfig, FullIPv6Prefix, c.IPv6Prefix)
	}
	return nil
}
