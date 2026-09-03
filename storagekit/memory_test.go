package storagekit_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func newMem() *storagekit.MemoryStorage {
	return storagekit.NewMemoryStorage(storagekit.NewURLBuilder("https://cdn.example.com", "media"))
}

func TestMemoryStorage_PutReturnsStableRef(t *testing.T) {
	m := newMem()
	body := []byte("hello-bytes")

	ref, err := m.Put(context.Background(), bytes.NewReader(body), "image/png", int64(len(body)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", ref.ContentType)
	}
	if ref.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", ref.Size, len(body))
	}
	if want := "https://cdn.example.com/media/" + ref.Key; ref.URL != want {
		t.Errorf("URL = %q, want %q", ref.URL, want)
	}
	if !strings.HasSuffix(ref.Key, ".png") {
		t.Errorf("Key = %q, want .png suffix", ref.Key)
	}

	got, ok := m.Get(ref.Key)
	if !ok || !bytes.Equal(got, body) {
		t.Errorf("Get(%q) = %q,%v want %q,true", ref.Key, got, ok, body)
	}
}

func TestMemoryStorage_PutRejectsSizeMismatch(t *testing.T) {
	m := newMem()
	body := []byte("0123456789")

	if _, err := m.Put(context.Background(), bytes.NewReader(body), "application/pdf", 4); !errors.Is(err, storagekit.ErrUploadFailed) {
		t.Fatalf("Put oversize: err = %v, want ErrUploadFailed", err)
	}
	if _, err := m.Put(context.Background(), bytes.NewReader(body), "application/pdf", 40); !errors.Is(err, storagekit.ErrUploadFailed) {
		t.Fatalf("Put short body: err = %v, want ErrUploadFailed", err)
	}
	if got, _ := m.Get(""); got != nil {
		t.Fatal("nothing must be stored on mismatch")
	}
}

func TestMemoryStorage_PutUnknownSizeStreamsToEOF(t *testing.T) {
	m := newMem()
	body := []byte("streamed")

	ref, err := m.Put(context.Background(), bytes.NewReader(body), "image/webp", -1)
	if err != nil {
		t.Fatalf("Put(-1): %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Errorf("Size = %d, want actual %d", ref.Size, len(body))
	}
}

func TestMemoryStorage_PresignedGet(t *testing.T) {
	m := newMem()
	url, err := m.PresignedGet(context.Background(), "ignored-bucket", "obj.ogg", 30*time.Second)
	if err != nil {
		t.Fatalf("PresignedGet: %v", err)
	}
	if want := "https://cdn.example.com/media/obj.ogg?x-presigned-ttl=30"; url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if _, err := m.PresignedGet(context.Background(), "b", "", time.Second); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Fatalf("empty key: err = %v, want ErrPresignFailed", err)
	}
}

func TestMemoryStorage_DeleteIsIdempotent(t *testing.T) {
	m := newMem()
	ref, err := m.Put(context.Background(), bytes.NewReader([]byte("x")), "application/pdf", 1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := m.Delete(context.Background(), ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := m.Get(ref.Key); ok {
		t.Error("object still present after Delete")
	}
	if err := m.Delete(context.Background(), ref.Key); err != nil {
		t.Fatalf("Delete missing: err = %v, want nil (idempotent)", err)
	}
}
