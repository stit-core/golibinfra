//go:build integration

package minio_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

const minioImage = "minio/minio:RELEASE.2025-01-20T14-49-07Z"

func newUploader(t *testing.T) (*skminio.Uploader, *miniogo.Client, string) {
	t.Helper()
	ctx := context.Background()

	container, err := tcminio.Run(ctx, minioImage)
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	const bucket = "test-bucket"
	cfg := storagekit.Config{
		Endpoint:      endpoint,
		AccessKey:     container.Username,
		SecretKey:     container.Password,
		Bucket:        bucket,
		UseSSL:        false,
		PublicBaseURL: "https://cdn.example.com",
	}

	client, err := miniogo.New(endpoint, &miniogo.Options{
		Creds:  credentials.NewStaticV4(container.Username, container.Password, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	if err := client.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}

	urls := storagekit.NewURLBuilder(cfg.PublicBaseURL, cfg.Bucket)
	return skminio.NewUploader(client, cfg, urls), client, bucket
}

func TestUploader_PutStatDelete_Integration(t *testing.T) {
	ctx := context.Background()
	up, client, bucket := newUploader(t)

	body := bytes.Repeat([]byte("%PDF-1.7\n"), 64)
	ref, err := up.Put(ctx, bytes.NewReader(body), "application/pdf", int64(len(body)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Key == "" || ref.Size != int64(len(body)) || ref.ContentType != "application/pdf" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if want := "https://cdn.example.com/" + bucket + "/" + ref.Key; ref.URL != want {
		t.Fatalf("URL = %q, want %q", ref.URL, want)
	}

	stat, err := client.StatObject(ctx, bucket, ref.Key, miniogo.StatObjectOptions{})
	if err != nil {
		t.Fatalf("stat object: %v", err)
	}
	if stat.Size != int64(len(body)) {
		t.Fatalf("stat size: got %d, want %d", stat.Size, len(body))
	}
	if stat.ContentType != "application/pdf" {
		t.Fatalf("stat content-type: got %q", stat.ContentType)
	}

	if err := up.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := client.StatObject(ctx, bucket, ref.Key, miniogo.StatObjectOptions{}); err == nil {
		t.Fatal("object still present after Delete")
	}
}

func TestUploader_PutRejectsOversize_Integration(t *testing.T) {
	ctx := context.Background()
	up, _, _ := newUploader(t)

	body := []byte("0123456789")
	_, err := up.Put(ctx, bytes.NewReader(body), "application/pdf", 4)
	if !errors.Is(err, storagekit.ErrUploadFailed) {
		t.Fatalf("Put oversize: err = %v, want ErrUploadFailed", err)
	}
}
