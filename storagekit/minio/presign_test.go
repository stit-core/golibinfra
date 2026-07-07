package minio_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
	skminio "git.stit.tech/stit-core/golibinfra/storagekit/minio"
)

func newPresigner(t *testing.T) *skminio.Uploader {
	t.Helper()
	cfg := storagekit.Config{
		Endpoint:  "localhost:9000",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		Bucket:    "recordings",
		Region:    "us-east-1",
	}
	client, err := skminio.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return skminio.NewUploader(client, cfg, storagekit.NewURLBuilder("https://cdn.example.com", cfg.Bucket))
}

// Presign SigV4 — локальная операция (Region задан → без bucket-location lookup),
// поэтому unit-тест не требует живого сервера.
func TestUploader_PresignedGet(t *testing.T) {
	u := newPresigner(t)
	url, err := u.PresignedGet(context.Background(), "recordings", "room-1/alice-123.ogg", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignedGet: %v", err)
	}
	for _, want := range []string{"recordings", "alice-123.ogg", "X-Amz-Signature", "X-Amz-Expires"} {
		if !strings.Contains(url, want) {
			t.Errorf("presigned url %q missing %q", url, want)
		}
	}
}

func TestUploader_PresignedGet_ValidatesArgs(t *testing.T) {
	u := newPresigner(t)
	if _, err := u.PresignedGet(context.Background(), "", "k", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("empty bucket: err = %v, want ErrPresignFailed", err)
	}
	if _, err := u.PresignedGet(context.Background(), "b", "", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("empty key: err = %v, want ErrPresignFailed", err)
	}
}
