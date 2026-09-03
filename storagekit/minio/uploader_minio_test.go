package minio_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

const minioImage = "minio/minio:RELEASE.2025-01-20T14-49-07Z"

func newUploader(t *testing.T) (*skminio.Uploader, *miniogo.Client, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("testcontainers (minio) — пропуск под -short")
	}
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

func listKeys(t *testing.T, client *miniogo.Client, bucket string) []string {
	t.Helper()
	var keys []string
	for obj := range client.ListObjects(context.Background(), bucket, miniogo.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			t.Fatalf("list objects: %v", obj.Err)
		}
		keys = append(keys, obj.Key)
	}
	return keys
}

func TestUploader_PutStatDelete(t *testing.T) {
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
	if stat.Size != int64(len(body)) || stat.ContentType != "application/pdf" {
		t.Fatalf("stat = %d/%q", stat.Size, stat.ContentType)
	}

	if err := up.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := client.StatObject(ctx, bucket, ref.Key, miniogo.StatObjectOptions{}); err == nil {
		t.Fatal("object still present after Delete")
	}
	if err := up.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete missing key: err = %v, want nil (idempotent)", err)
	}
}

func TestUploader_PutStreamsUnknownSize(t *testing.T) {
	ctx := context.Background()
	up, client, bucket := newUploader(t)

	body := bytes.Repeat([]byte("x"), 100_000)
	ref, err := up.Put(ctx, bytes.NewReader(body), "application/octet-stream", -1)
	if err != nil {
		t.Fatalf("Put(-1): %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", ref.Size, len(body))
	}
	obj, err := client.GetObject(ctx, bucket, ref.Key, miniogo.GetObjectOptions{})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	got, err := io.ReadAll(obj)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored body differs (len %d, err %v)", len(got), err)
	}
}

// stream скрывает ReaderAt/Seeker: minio-go читает такой источник
// последовательно — вторая ветка контракта size.
type stream struct{ io.Reader }

func TestUploader_PutRejectsSizeMismatch(t *testing.T) {
	ctx := context.Background()
	up, client, bucket := newUploader(t)

	body := []byte("0123456789")
	readers := map[string]func() io.Reader{
		"readerat": func() io.Reader { return bytes.NewReader(body) },
		"stream":   func() io.Reader { return stream{bytes.NewReader(body)} },
	}
	for name, mk := range readers {
		if _, err := up.Put(ctx, mk(), "application/pdf", 4); !errors.Is(err, storagekit.ErrUploadFailed) {
			t.Errorf("%s: Put oversize: err = %v, want ErrUploadFailed", name, err)
		}
		if _, err := up.Put(ctx, mk(), "application/pdf", 40); !errors.Is(err, storagekit.ErrUploadFailed) {
			t.Errorf("%s: Put short body: err = %v, want ErrUploadFailed", name, err)
		}
	}
	if keys := listKeys(t, client, bucket); len(keys) != 0 {
		t.Fatalf("bucket must stay empty after rejected uploads, got %v", keys)
	}

	ref, err := up.Put(ctx, stream{bytes.NewReader(body)}, "application/pdf", int64(len(body)))
	if err != nil || ref.Size != int64(len(body)) {
		t.Fatalf("stream exact size: ref = %+v, err = %v", ref, err)
	}
}

func TestUploader_EmitsMetrics(t *testing.T) {
	ctx := context.Background()
	up, _, _ := newUploader(t)

	before := snapshot(t)
	ref, err := up.Put(ctx, bytes.NewReader([]byte("m")), "image/png", 1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := up.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := up.PresignedGet(ctx, "", "", 0); err == nil {
		t.Fatal("presign with empty args must fail")
	}
	after := snapshot(t)

	for _, k := range []string{"put/ok", "delete/ok", "presign/error"} {
		if got := after.ops[k] - before.ops[k]; got != 1 {
			t.Errorf("storage_ops_total{%s} delta = %d, want 1", k, got)
		}
	}
	for _, op := range []string{"put", "delete", "presign"} {
		if got := after.durations[op] - before.durations[op]; got != 1 {
			t.Errorf("storage_op_duration_seconds{op=%s} count delta = %d, want 1", op, got)
		}
	}
}
