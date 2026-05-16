// Package minio — боевой S3-совместимый адаптер storagekit.Storage поверх
// minio-go. Доменно-нейтрален: стримит байты как есть, image/file-валидация
// (mime, размеры, декод) — на стороне приложения.
package minio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	miniogo "github.com/minio/minio-go/v7"

	"git.stit.tech/stit-infra/golibinfra/storagekit"
)

type Uploader struct {
	client *miniogo.Client
	bucket string
	urls   *storagekit.URLBuilder
}

func NewUploader(client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder) *Uploader {
	return &Uploader{client: client, bucket: cfg.Bucket, urls: urls}
}

func (u *Uploader) Put(ctx context.Context, r io.Reader, contentType string, size int64) (storagekit.Ref, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return storagekit.Ref{}, fmt.Errorf("%w: read body: %w", storagekit.ErrUploadFailed, err)
	}
	if int64(len(data)) > size {
		return storagekit.Ref{}, fmt.Errorf("%w: body larger than declared %d", storagekit.ErrUploadFailed, size)
	}

	key, err := storagekit.NewKey(contentType)
	if err != nil {
		return storagekit.Ref{}, err
	}

	if _, err = u.client.PutObject(ctx, u.bucket, key, bytes.NewReader(data), int64(len(data)), miniogo.PutObjectOptions{
		ContentType: contentType,
	}); err != nil {
		return storagekit.Ref{}, fmt.Errorf("%w: put object: %w", storagekit.ErrUploadFailed, err)
	}

	return storagekit.Ref{
		Key:         key,
		URL:         u.urls.Build(key),
		ContentType: contentType,
		Size:        int64(len(data)),
	}, nil
}

func (u *Uploader) Delete(ctx context.Context, key string) error {
	if err := u.client.RemoveObject(ctx, u.bucket, key, miniogo.RemoveObjectOptions{}); err != nil {
		var resp miniogo.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
			return fmt.Errorf("%w: %q", storagekit.ErrNotFound, key)
		}
		return fmt.Errorf("%w: remove object %q: %w", storagekit.ErrUploadFailed, key, err)
	}
	return nil
}
