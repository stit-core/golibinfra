package minio

import (
	"context"
	"fmt"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func NewClient(cfg storagekit.Config) (*miniogo.Client, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("storagekit/minio: STORAGE_S3_ENDPOINT is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("storagekit/minio: STORAGE_S3_BUCKET is required")
	}
	client, err := miniogo.New(cfg.Endpoint, &miniogo.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storagekit/minio: new client: %w", err)
	}
	return client, nil
}

func Probe(client *miniogo.Client, cfg storagekit.Config) healthkit.Probe {
	return func(ctx context.Context) error {
		ok, err := client.BucketExists(ctx, cfg.Bucket)
		if err != nil {
			return fmt.Errorf("storagekit/minio: bucket exists check: %w", err)
		}
		if !ok {
			return fmt.Errorf("storagekit/minio: bucket %q not found", cfg.Bucket)
		}
		return nil
	}
}

// Module — боевой Storage-провайдер. Подключается рядом с storagekit.Module
// (тот даёт URLBuilder). Config caller регистрирует сам.
var Module = fx.Module(
	"storagekit/minio",
	fx.Provide(
		NewClient,
		NewUploader,
		func(u *Uploader) storagekit.Storage { return u },
		func(u *Uploader) storagekit.Presigner { return u },
	),
)
