// Package config — композиция загрузчиков конфигурации: интерфейс
// Source (Apply(ctx, dst)) реализует env-источник (envconfig), Load
// прогоняет dst через каждый переданный Source по порядку. Поздние
// Source'ы перезатирают поля более ранних. Caller может добавить
// собственные Source'ы (например, из внешнего секрет-стора).
package config

import (
	"context"
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

type Source interface {
	Apply(ctx context.Context, dst any) error
}

func Load(ctx context.Context, dst any, sources ...Source) error {
	for i, src := range sources {
		if err := src.Apply(ctx, dst); err != nil {
			return fmt.Errorf("config: source[%d]: %w", i, err)
		}
	}
	return nil
}

type envSource struct{ prefix string }

func Env(prefix ...string) Source {
	p := ""
	if len(prefix) > 0 {
		p = prefix[0]
	}
	return &envSource{prefix: p}
}

func (s *envSource) Apply(_ context.Context, dst any) error {
	return envconfig.Process(s.prefix, dst)
}
