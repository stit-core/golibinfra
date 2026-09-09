package testkit_test

import (
	"bytes"
	"testing"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"git.stit.tech/stit-core/golibinfra/testkit"
)

func TestStartMinIO(t *testing.T) {
	fix := testkit.StartMinIO(t)
	if fix.Endpoint == "" || fix.AccessKey == "" || fix.SecretKey == "" || fix.Container == nil {
		t.Fatalf("fixture = %+v", fix)
	}

	client, err := miniogo.New(fix.Endpoint, &miniogo.Options{
		Creds: credentials.NewStaticV4(fix.AccessKey, fix.SecretKey, ""),
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}

	const bucket = "testkit"
	if err := client.MakeBucket(t.Context(), bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	body := []byte("payload")
	if _, err := client.PutObject(t.Context(), bucket, "obj", bytes.NewReader(body),
		int64(len(body)), miniogo.PutObjectOptions{}); err != nil {
		t.Fatalf("put object: %v", err)
	}
	stat, err := client.StatObject(t.Context(), bucket, "obj", miniogo.StatObjectOptions{})
	if err != nil {
		t.Fatalf("stat object: %v", err)
	}
	if stat.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", stat.Size, len(body))
	}
}
