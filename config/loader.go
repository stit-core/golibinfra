// Package config — загрузка типизированных Config-структур из ENV и/или Vault.
//
// Источники применяются по очереди: каждый следующий перекрывает значения предыдущего.
// Типичный порядок: env → vault. Если Vault недоступен (локалка) — fallback на env.
package config

import (
	"context"
	"fmt"

	"github.com/kelseyhightower/envconfig"
)

// Source — заполняет поля dst по тегам (`envconfig:"..."` для env-source, `vault:"path/key"` для vault-source).
type Source interface {
	Apply(ctx context.Context, dst any) error
}

// Load прогоняет dst через все источники по порядку и валидирует результат.
//
// Использование:
//
//	var cfg AppConfig
//	if err := config.Load(ctx, &cfg, config.Env(), config.Vault(client, "secret/data/myapp")); err != nil { ... }
func Load(ctx context.Context, dst any, sources ...Source) error {
	for i, src := range sources {
		if err := src.Apply(ctx, dst); err != nil {
			return fmt.Errorf("config: source[%d]: %w", i, err)
		}
	}
	return nil
}

type envSource struct{ prefix string }

// Env — источник из переменных окружения. prefix — например, "APP" → ENV-ключи будут APP_*.
// Пустой prefix = без префикса.
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
