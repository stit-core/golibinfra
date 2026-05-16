package storagekit

import "go.uber.org/fx"

// Module provide'ит URLBuilder из Config. Storage-провайдер (боевой
// S3-адаптер) регистрирует подпакет storagekit/minio своим Module —
// однородно с pattern media/minio. Config caller регистрирует сам
// (asymmetry note, см. cachekit/pgkit).
var Module = fx.Module(
	"storagekit",
	fx.Provide(
		func(cfg Config) *URLBuilder {
			return NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket)
		},
	),
)
