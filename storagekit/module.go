package storagekit

import "go.uber.org/fx"

// Module даёт *URLBuilder из Config. Боевой Storage регистрирует подпакет
// storagekit/minio своим Module; Config caller провайдит сам.
var Module = fx.Module(
	"storagekit",
	fx.Provide(
		func(cfg Config) *URLBuilder {
			return NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket, cfg.URLStyle)
		},
	),
)
