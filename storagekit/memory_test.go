package storagekit_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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

func TestMemoryStorage_PutRejectsOversizeBody(t *testing.T) {
	m := newMem()
	body := []byte("0123456789")

	_, err := m.Put(context.Background(), bytes.NewReader(body), "application/pdf", 4)
	if !errors.Is(err, storagekit.ErrUploadFailed) {
		t.Fatalf("Put oversize: err = %v, want ErrUploadFailed", err)
	}
}

func TestMemoryStorage_Delete(t *testing.T) {
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
	if err := m.Delete(context.Background(), ref.Key); !errors.Is(err, storagekit.ErrNotFound) {
		t.Fatalf("Delete missing: err = %v, want ErrNotFound", err)
	}
}
