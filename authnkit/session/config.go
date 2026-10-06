package session

import (
	"errors"
	"fmt"
	"time"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// ErrConfig — невалидный конфиг сессий; возвращается Validate, NewConfig и
// конструкторами хранилищ.
var ErrConfig = errors.New("authnkit/session: invalid config")

// Config — сроки сессий. Имена полей и envconfig-теги — часть public API.
type Config struct {
	// IdleTTL — срок refresh-токена: ротация выдаёт следующий на тот же срок, но не
	// дальше AbsoluteTTL сессии.
	IdleTTL time.Duration `envconfig:"SESSION_IDLE_TTL" default:"168h"`
	// AbsoluteTTL — срок сессии от входа; ротация его не продлевает.
	AbsoluteTTL time.Duration `envconfig:"SESSION_ABSOLUTE_TTL" default:"720h"`
	// CleanupInterval — период job'а очистки истёкших сессий, который регистрирует
	// pgstore.Module.
	CleanupInterval time.Duration `envconfig:"SESSION_CLEANUP_INTERVAL" default:"1h"`
}

// NewConfig читает Config из ENV и валидирует его.
func NewConfig() (Config, error) {
	var c Config
	if err := envconf.Load(&c); err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return c, c.Validate()
}

// Validate проверяет сроки; нарушение — ErrConfig. Нулевой Config отвергается:
// сессия без срока жизни хуже упавшего старта.
func (c Config) Validate() error {
	switch {
	case c.IdleTTL <= 0:
		return fmt.Errorf("%w: SESSION_IDLE_TTL must be > 0, got %s", ErrConfig, c.IdleTTL)
	case c.AbsoluteTTL < c.IdleTTL:
		return fmt.Errorf("%w: SESSION_ABSOLUTE_TTL (%s) must be >= SESSION_IDLE_TTL (%s)", ErrConfig, c.AbsoluteTTL, c.IdleTTL)
	case c.CleanupInterval <= 0:
		return fmt.Errorf("%w: SESSION_CLEANUP_INTERVAL must be > 0, got %s", ErrConfig, c.CleanupInterval)
	}
	return nil
}
