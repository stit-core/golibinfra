package storagekit

import "go.uber.org/fx"

// Module даёт *URLBuilder из Config; невалидный Config — ErrConfig при сборке графа. Боевой
// Storage регистрирует подпакет storagekit/minio своим Module; Config caller провайдит сам.
var Module = fx.Module(
	"storagekit",
	fx.Provide(
		func(cfg Config) (*URLBuilder, error) {
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle), nil
		},
	),
)
