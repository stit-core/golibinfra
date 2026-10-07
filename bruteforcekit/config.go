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

// SubjectBudgetOff — Config.SubjectMaxFailures без бюджета субъекта: учёт только по паре.
const SubjectBudgetOff = -1

// Config — политика guard'а. Имена полей и envconfig-теги — часть public API.
type Config struct {
	// MaxFailures — число неудач в окне, после которого пара блокируется.
	MaxFailures int `envconfig:"BRUTEFORCE_MAX_FAILURES" default:"5"`
	// LockDuration — длительность блокировки от момента достижения порога.
	LockDuration time.Duration `envconfig:"BRUTEFORCE_LOCK_DURATION" default:"15m"`
	// SubjectMaxFailures — попытки одного subject по всем адресам в окне, после которых он
	// блокируется на SubjectLockDuration: перебор аккаунта с многих адресов. SubjectBudgetOff —
	// без бюджета субъекта.
	SubjectMaxFailures int `envconfig:"BRUTEFORCE_SUBJECT_MAX_FAILURES" default:"20"`
	// SubjectLockDuration — блокировка субъекта; короткая: держать чужой аккаунт закрытым можно
	// лишь непрерывным потоком неудач.
	SubjectLockDuration time.Duration `envconfig:"BRUTEFORCE_SUBJECT_LOCK_DURATION" default:"1m"`
	// CleanupRetention — возраст разблокированной строки, после которого её удаляет Cleanup; не
	// меньше LockDuration и SubjectLockDuration: иначе недобранное окно сбрасывалось бы быстрее,
	// чем кончается блокировка.
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
	case c.SubjectMaxFailures < 1 && c.SubjectMaxFailures != SubjectBudgetOff:
		return fmt.Errorf("%w: BRUTEFORCE_SUBJECT_MAX_FAILURES must be >= 1 or %d (off), got %d", ErrConfig, SubjectBudgetOff, c.SubjectMaxFailures)
	case c.SubjectLockDuration <= 0:
		return fmt.Errorf("%w: BRUTEFORCE_SUBJECT_LOCK_DURATION must be > 0, got %s", ErrConfig, c.SubjectLockDuration)
	case c.CleanupRetention < c.LockDuration || c.CleanupRetention < c.SubjectLockDuration:
		return fmt.Errorf("%w: BRUTEFORCE_CLEANUP_RETENTION (%s) must be >= BRUTEFORCE_LOCK_DURATION (%s) and BRUTEFORCE_SUBJECT_LOCK_DURATION (%s): cleanup would reset an unlocked window faster than a lock ends",
			ErrConfig, c.CleanupRetention, c.LockDuration, c.SubjectLockDuration)
	case c.CleanupInterval <= 0:
		return fmt.Errorf("%w: BRUTEFORCE_CLEANUP_INTERVAL must be > 0, got %s", ErrConfig, c.CleanupInterval)
	case c.IPv6Prefix < 1 || c.IPv6Prefix > FullIPv6Prefix:
		return fmt.Errorf("%w: BRUTEFORCE_IPV6_PREFIX must be in [1, %d], got %d", ErrConfig, FullIPv6Prefix, c.IPv6Prefix)
	}
	return nil
}
