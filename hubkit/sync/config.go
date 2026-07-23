package sync

import "time"

// Config — env-конфиг движка синка (общий для всех ресурсов сервиса).
type Config struct {
	// Interval — период pull-тика.
	Interval time.Duration `default:"30s" envconfig:"HUB_SYNC_INTERVAL"`
	// Limit — размер страницы фида (и признак «догнали»: короткая страница = конец).
	Limit int `default:"500" envconfig:"HUB_SYNC_LIMIT"`
}
