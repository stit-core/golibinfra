// Package minio — боевой S3-совместимый адаптер storagekit поверх minio-go:
// Storage (Put/Get/Stat/Delete), Presigner, health-проба бакета и fx-модуль.
package minio

import (
	"fmt"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

// NewClient строит клиент minio-go из проверенного Config: невалидная
// конфигурация валит старт приложения (ErrConfig), а не первый запрос.
func NewClient(cfg storagekit.Config) (*miniogo.Client, error) {
	addr, secure, err := cfg.EndpointAddr()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client, err := miniogo.New(addr, &miniogo.Options{
		Creds:        credentialsFor(cfg),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: bucketLookup(cfg.BucketLookup),
		// minio-go считает попытки, конфиг — повторы после первой попытки.
		MaxRetries: cfg.MaxRetries + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: new client: %w", storagekit.ErrConfig, err)
	}
	return client, nil
}

func credentialsFor(cfg storagekit.Config) *credentials.Credentials {
	if cfg.Credentials == storagekit.CredentialsChain {
		return credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.EnvMinio{},
			&credentials.IAM{},
		})
	}
	return credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
}

func bucketLookup(style string) miniogo.BucketLookupType {
	switch style {
	case storagekit.BucketLookupPath:
		return miniogo.BucketLookupPath
	case storagekit.BucketLookupDNS:
		return miniogo.BucketLookupDNS
	default:
		return miniogo.BucketLookupAuto
	}
}
