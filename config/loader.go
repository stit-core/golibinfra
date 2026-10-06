// Package config композирует источники значений конфигурации: Source
// заполняет произвольную структуру, Load прогоняет её через переданные
// источники по порядку. Штатный источник — переменные окружения, тем же ридером, что у китов.
package config

import (
	"errors"
	"fmt"

	"git.stit.tech/stit-core/golibinfra/internal/envconf"
)

// Source заполняет переданную структуру значениями из своего хранилища. Load
// зовут на сборке графа, где запроса нет: источник с I/O (файл, remote) сам
// ограничивает своё время.
type Source interface {
	// Apply записывает значения в dst — указатель на struct.
	Apply(dst any) error
}

// Load прогоняет dst через каждый Source по порядку: поздний источник
// перезатирает поля раннего. Применяются все источники; ошибки собираются
// в одну через errors.Join, каждая — с индексом своего источника.
func Load(dst any, sources ...Source) error {
	var errs []error
	for i, src := range sources {
		if err := src.Apply(dst); err != nil {
			errs = append(errs, fmt.Errorf("config: source[%d]: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// Env возвращает источник, читающий переменные окружения с именами envconfig-тегов: пустая
// переменная — как незаданная, поле без тега и без ignored:"true" — ошибка.
func Env() Source { return envSource{} }

// EnvPrefixed возвращает источник, читающий переменные окружения с приставкой
// PREFIX_; при незаданной PREFIX_NAME значение берётся из NAME.
func EnvPrefixed(prefix string) Source { return envSource{prefix: prefix} }

type envSource struct{ prefix string }

func (s envSource) Apply(dst any) error {
	return envconf.LoadPrefixed(dst, s.prefix, true)
}
