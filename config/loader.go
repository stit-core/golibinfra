// Package config композирует источники значений конфигурации: Source
// заполняет произвольную структуру, Load прогоняет её через переданные
// источники по порядку. Штатный источник — переменные окружения (envconfig).
package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

// Source заполняет переданную структуру значениями из своего хранилища.
type Source interface {
	// Apply записывает значения в dst — указатель на struct.
	Apply(ctx context.Context, dst any) error
}

// Load прогоняет dst через каждый Source по порядку: поздний источник
// перезатирает поля раннего. Применяются все источники; ошибки собираются
// в одну через errors.Join, каждая — с индексом своего источника.
func Load(ctx context.Context, dst any, sources ...Source) error {
	var errs []error
	for i, src := range sources {
		if err := src.Apply(ctx, dst); err != nil {
			errs = append(errs, fmt.Errorf("config: source[%d]: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// Env возвращает источник, читающий переменные окружения по envconfig-тегам
// без приставки.
func Env() Source { return envSource{} }

// EnvPrefixed возвращает источник, читающий переменные окружения с приставкой
// PREFIX_; при отсутствии PREFIX_NAME значение берётся из NAME.
func EnvPrefixed(prefix string) Source { return envSource{prefix: prefix} }

type envSource struct{ prefix string }

func (s envSource) Apply(_ context.Context, dst any) error {
	return envconfig.Process(s.prefix, dst)
}
