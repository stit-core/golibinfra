// Package minio — боевой S3-совместимый адаптер storagekit.Storage поверх
// minio-go. Доменно-нейтрален: стримит байты как есть, image/file-валидация
// (mime, размеры, декод) — на стороне приложения.
package minio

import (
	"context"
	"fmt"
	"io"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/internal/otelkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
)

var (
	opsTotal = otelkit.Must(otelkit.Meter("storagekit").Int64Counter(
		"storage_ops_total",
		metric.WithDescription("Object storage operations by result"),
	))
	opDuration = otelkit.Must(otelkit.Meter("storagekit").Float64Histogram(
		"storage_op_duration_seconds",
		metric.WithDescription("Object storage operation duration"),
		metric.WithUnit("s"),
	))
)

func observe(ctx context.Context, op string, start time.Time, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	opDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.String("op", op)))
	opsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op), attribute.String("result", result)))
}

type Uploader struct {
	client *miniogo.Client
	bucket string
	urls   *storagekit.URLBuilder
}

func NewUploader(client *miniogo.Client, cfg storagekit.Config, urls *storagekit.URLBuilder) *Uploader {
	return &Uploader{client: client, bucket: cfg.Bucket, urls: urls}
}

// Put стримит тело в PutObject без буферизации в памяти. При известном size
// клиент читает ровно size байт, поэтому хвост длиннее заявленного ловится
// пробным чтением после загрузки — объект в этом случае удаляется.
func (u *Uploader) Put(ctx context.Context, r io.Reader, contentType string, size int64) (ref storagekit.Ref, err error) {
	start := time.Now()
	defer func() { observe(ctx, "put", start, err) }()

	key, err := storagekit.NewKey(contentType)
	if err != nil {
		return storagekit.Ref{}, err
	}
	offset := currentOffset(r)
	info, err := u.client.PutObject(ctx, u.bucket, key, r, size, miniogo.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return storagekit.Ref{}, fmt.Errorf("%w: put object: %w", storagekit.ErrUploadFailed, err)
	}
	if size >= 0 && hasTail(r, offset, size) {
		_ = u.client.RemoveObject(ctx, u.bucket, key, miniogo.RemoveObjectOptions{})
		return storagekit.Ref{}, fmt.Errorf("%w: body larger than declared %d", storagekit.ErrUploadFailed, size)
	}

	return storagekit.Ref{
		Key:         key,
		URL:         u.urls.Build(key),
		ContentType: contentType,
		Size:        info.Size,
	}, nil
}

func currentOffset(r io.Reader) int64 {
	if s, ok := r.(io.Seeker); ok {
		if off, err := s.Seek(0, io.SeekCurrent); err == nil {
			return off
		}
	}
	return 0
}

// hasTail: ReaderAt+Seeker источники minio-go читает через SectionReader, не
// двигая позицию, — хвост ищется ReadAt за границей size; потоковые читаются
// ровно на size байт — хвост ищется обычным Read.
func hasTail(r io.Reader, offset, size int64) bool {
	var probe [1]byte
	if ra, ok := r.(io.ReaderAt); ok {
		if _, seekable := r.(io.Seeker); seekable {
			n, _ := ra.ReadAt(probe[:], offset+size)
			return n > 0
		}
	}
	n, _ := r.Read(probe[:])
	return n > 0
}

// PresignedGet подписывает GET-ссылку на bucket/key со сроком ttl. Операция
// локальная (HMAC над кредами) — сеть не дёргается, если у клиента задан Region
// (иначе minio-go разово резолвит bucket-location).
func (u *Uploader) PresignedGet(ctx context.Context, bucket, key string, ttl time.Duration) (url string, err error) {
	start := time.Now()
	defer func() { observe(ctx, "presign", start, err) }()

	if bucket == "" || key == "" {
		return "", fmt.Errorf("%w: bucket and key required", storagekit.ErrPresignFailed)
	}
	signed, err := u.client.PresignedGetObject(ctx, bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %q/%q: %w", storagekit.ErrPresignFailed, bucket, key, err)
	}
	return signed.String(), nil
}

// Delete идемпотентен: S3 отвечает 204 и на отсутствующий ключ.
func (u *Uploader) Delete(ctx context.Context, key string) (err error) {
	start := time.Now()
	defer func() { observe(ctx, "delete", start, err) }()

	if err := u.client.RemoveObject(ctx, u.bucket, key, miniogo.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("%w: remove object %q: %w", storagekit.ErrDeleteFailed, key, err)
	}
	return nil
}
