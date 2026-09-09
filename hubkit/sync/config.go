package sync

import (
	"errors"
	"fmt"
	"time"
)

// ErrConfig — невалидный конфиг движка синка.
var ErrConfig = errors.New("hubkit/sync: invalid config")

// Config — env-конфиг движка синка (общий для всех ресурсов сервиса).
type Config struct {
	// Interval — период pull-тика, отсчитывается от конца предыдущего.
	Interval time.Duration `envconfig:"HUB_SYNC_INTERVAL" default:"30s"`
	// Limit — размер страницы фида (и признак «догнали»: короткая страница = конец).
	Limit int `envconfig:"HUB_SYNC_LIMIT" default:"500"`
	// Timeout — дедлайн одного тика; 0 — без дедлайна.
	Timeout time.Duration `envconfig:"HUB_SYNC_TIMEOUT" default:"5m"`
	// Jitter — случайная добавка [0, Jitter) к каждому ожиданию: движки
	// нескольких ресурсов не бьют в хаб одной пачкой.
	Jitter time.Duration `envconfig:"HUB_SYNC_JITTER" default:"5s"`
	// RunOnStart — первый тик сразу на старте, а не через Interval: после
	// деплоя проекции наполняются, не дожидаясь первого периода.
	RunOnStart bool `envconfig:"HUB_SYNC_RUN_ON_START" default:"true"`
}

// Validate проверяет параметры тика. Limit <= 0 сделал бы условие «короткая
// страница» недостижимым — движок ходил бы за лишней пустой страницей каждый тик.
func (c Config) Validate() error {
	if c.Interval <= 0 {
		return fmt.Errorf("%w: HUB_SYNC_INTERVAL must be > 0", ErrConfig)
	}
	if c.Limit <= 0 {
		return fmt.Errorf("%w: HUB_SYNC_LIMIT must be > 0", ErrConfig)
	}
	if c.Limit > maxLimit {
		return fmt.Errorf("%w: HUB_SYNC_LIMIT must be <= %d", ErrConfig, maxLimit)
	}
	if c.Timeout < 0 {
		return fmt.Errorf("%w: HUB_SYNC_TIMEOUT must be >= 0", ErrConfig)
	}
	if c.Jitter < 0 {
		return fmt.Errorf("%w: HUB_SYNC_JITTER must be >= 0", ErrConfig)
	}
	return nil
}

// maxLimit — верхняя граница страницы фида: хаб отдаёт страницу целиком в
// памяти, а тик держит её батчем в транзакции проекции.
const maxLimit = 10000
