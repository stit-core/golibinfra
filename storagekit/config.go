package storagekit

import "github.com/kelseyhightower/envconfig"

type Config struct {
	Endpoint      string `envconfig:"STORAGE_S3_ENDPOINT"`
	AccessKey     string `envconfig:"STORAGE_S3_ACCESS_KEY"`
	SecretKey     string `envconfig:"STORAGE_S3_SECRET_KEY"`
	Bucket        string `envconfig:"STORAGE_S3_BUCKET"`
	UseSSL        bool   `default:"false"                  envconfig:"STORAGE_S3_USE_SSL"`
	PublicBaseURL string `envconfig:"STORAGE_PUBLIC_BASE_URL"`
}

// NewConfig — exported, но НЕ в Module (asymmetry note: caller сам
// регистрирует Config, как у cachekit/pgkit/natskit).
func NewConfig() (Config, error) {
	var c Config
	return c, envconfig.Process("", &c)
}
