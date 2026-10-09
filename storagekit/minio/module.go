package minio

import (
	"context"
	"fmt"

	miniogo "github.com/minio/minio-go/v7"
	"go.uber.org/fx"

	"git.stit.tech/stit-core/golibinfra/healthkit"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
)

// ProbeName — имя health-пробы кита в группе `healthkit.probes`.
const ProbeName = "storagekit"

// Probe — проба существования бакета с собственным дедлайном
// (STORAGE_S3_PROBE_TIMEOUT): деградация S3 не держит /ready дольше него.
// Регистрируется потребителем через healthkit.ProvideProbe.
func Probe(client *miniogo.Client, cfg storagekit.Config) healthkit.Probe {
	probe := healthkit.New(ProbeName, func(ctx context.Context) error {
		return checkBucket(ctx, client, cfg.Bucket)
	})
	return healthkit.WithTimeout(probe, cfg.ProbeTimeout)
}

// Module — боевой Storage/Presigner поверх MinIO/S3. Подключается рядом с
// storagekit.Module (тот даёт URLBuilder); Config caller провайдит сам.
var Module = fx.Module(
	"storagekit/minio",
	fx.Provide(
		NewClient,
		NewStorage,
		func(s *Storage) storagekit.Storage { return s },
		func(s *Storage) storagekit.Presigner { return s },
	),
	fx.Invoke(registerStartupCheck),
)

func registerStartupCheck(lc fx.Lifecycle, client *miniogo.Client, cfg storagekit.Config, log logkit.Logger) {
	if !cfg.CreateBucket && !cfg.ProbeOnStart {
		return
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if cfg.ProbeTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, cfg.ProbeTimeout)
				defer cancel()
			}
			if cfg.CreateBucket {
				if err := ensureBucket(ctx, client, cfg); err != nil {
					return err
				}
			}
			if !cfg.ProbeOnStart {
				return nil
			}
			if err := checkBucket(ctx, client, cfg.Bucket); err != nil {
				return err
			}
			log.Info(ctx, "storagekit: bucket ready", "bucket", cfg.Bucket)
			return nil
		},
	})
}

func ensureBucket(ctx context.Context, client *miniogo.Client, cfg storagekit.Config) error {
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return fmt.Errorf("storagekit/minio: bucket exists check %q: %w", cfg.Bucket, err)
	}
	if exists {
		return nil
	}
	err = client.MakeBucket(ctx, cfg.Bucket, miniogo.MakeBucketOptions{Region: cfg.Region})
	// Между проверкой и созданием бакет создал соседний инстанс, стартующий одновременно: он
	// уже наш — это не отказ старта.
	if err != nil && miniogo.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
		return fmt.Errorf("storagekit/minio: create bucket %q: %w", cfg.Bucket, err)
	}
	return nil
}

func checkBucket(ctx context.Context, client *miniogo.Client, bucket string) error {
	ok, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("storagekit/minio: bucket exists check %q: %w", bucket, err)
	}
	if !ok {
		return fmt.Errorf("storagekit/minio: bucket %q not found", bucket)
	}
	return nil
}
