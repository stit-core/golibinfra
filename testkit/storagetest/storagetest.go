// Package storagetest — общий контрактный набор для реализаций
// storagekit.Storage и storagekit.Presigner: боевой адаптер и in-memory
// проверяются одними и теми же кейсами.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

// Subject — реализация под контрактом. Keys опционален: если задан, контракт
// дополнительно проверяет, что отвергнутые загрузки не оставляют объектов.
type Subject struct {
	Storage   storagekit.Storage
	Presigner storagekit.Presigner
	Keys      func(t *testing.T) []string
}

// RunContract прогоняет контракт Storage/Presigner. newSubject вызывается на
// каждый кейс и должен отдавать изолированное хранилище.
func RunContract(t *testing.T, newSubject func(t *testing.T) Subject) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, s Subject)
	}{
		{"PutGetStatDelete", putGetStatDelete},
		{"PutUnknownSize", putUnknownSize},
		{"PutRejectsSizeMismatch", putRejectsSizeMismatch},
		{"PutNormalizesContentType", putNormalizesContentType},
		{"MissingObject", missingObject},
		{"EmptyKeyRejected", emptyKeyRejected},
		{"PresignValidatesArgs", presignValidatesArgs},
		{"PresignGetAndPut", presignGetAndPut},
		{"ContextCancelled", contextCancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newSubject(t))
		})
	}
}

func putGetStatDelete(t *testing.T, s Subject) {
	ctx := t.Context()
	body := bytes.Repeat([]byte("%PDF-1.7\n"), 64)

	ref, err := s.Storage.Put(ctx, bytes.NewReader(body), "application/pdf", int64(len(body)))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.Size != int64(len(body)) || ref.ContentType != "application/pdf" {
		t.Fatalf("ref = %+v", ref)
	}
	if !strings.HasSuffix(ref.Key, ".pdf") {
		t.Errorf("Key = %q, want .pdf suffix", ref.Key)
	}
	if !strings.HasSuffix(ref.URL, ref.Key) {
		t.Errorf("URL = %q, want suffix %q", ref.URL, ref.Key)
	}

	rc, info, err := s.Storage.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get body: len %d, err %v", len(got), err)
	}
	if info.Size != int64(len(body)) || info.ContentType != "application/pdf" {
		t.Errorf("Get info = %+v", info)
	}
	if info.ETag == "" {
		t.Error("Get info.ETag is empty")
	}
	if info.LastModified.IsZero() {
		t.Error("Get info.LastModified is zero")
	}

	stat, err := s.Storage.Stat(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != info.Size || stat.ContentType != info.ContentType {
		t.Errorf("Stat = %+v, want %+v", stat, info)
	}

	if err := s.Storage.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Storage.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("Delete missing key: %v, want nil (idempotent)", err)
	}
	if _, err := s.Storage.Stat(ctx, ref.Key); !errors.Is(err, storagekit.ErrNotFound) {
		t.Fatalf("Stat after Delete: %v, want ErrNotFound", err)
	}
}

func putUnknownSize(t *testing.T, s Subject) {
	ctx := t.Context()
	body := bytes.Repeat([]byte("x"), 100_000)

	ref, err := s.Storage.Put(ctx, hiddenLen{bytes.NewReader(body)}, "application/octet-stream", -1)
	if err != nil {
		t.Fatalf("Put(-1): %v", err)
	}
	if ref.Size != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", ref.Size, len(body))
	}

	rc, _, err := s.Storage.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored body differs: len %d, err %v", len(got), err)
	}
}

func putRejectsSizeMismatch(t *testing.T, s Subject) {
	ctx := t.Context()
	body := []byte("0123456789")

	sources := map[string]func() io.Reader{
		"seekable": func() io.Reader { return bytes.NewReader(body) },
		"stream":   func() io.Reader { return hiddenLen{bytes.NewReader(body)} },
	}
	for name, mk := range sources {
		for _, declared := range []int64{4, 40} {
			_, err := s.Storage.Put(ctx, mk(), "application/pdf", declared)
			if !errors.Is(err, storagekit.ErrSizeMismatch) {
				t.Errorf("%s: Put(size=%d): err = %v, want ErrSizeMismatch", name, declared, err)
			}
			if !errors.Is(err, storagekit.ErrUploadFailed) {
				t.Errorf("%s: Put(size=%d): err = %v, want ErrUploadFailed", name, declared, err)
			}
		}
	}
	if s.Keys != nil {
		if keys := s.Keys(t); len(keys) != 0 {
			t.Fatalf("rejected uploads left objects: %v", keys)
		}
	}

	ref, err := s.Storage.Put(ctx, hiddenLen{bytes.NewReader(body)}, "application/pdf", int64(len(body)))
	if err != nil || ref.Size != int64(len(body)) {
		t.Fatalf("stream with exact size: ref = %+v, err = %v", ref, err)
	}
}

func putNormalizesContentType(t *testing.T, s Subject) {
	ctx := t.Context()

	ref, err := s.Storage.Put(ctx, strings.NewReader("payload"), "", 7)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if ref.ContentType != storagekit.DefaultContentType {
		t.Errorf("Ref.ContentType = %q, want %q", ref.ContentType, storagekit.DefaultContentType)
	}
	stat, err := s.Storage.Stat(ctx, ref.Key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.ContentType != storagekit.DefaultContentType {
		t.Errorf("stored content-type = %q, want %q", stat.ContentType, storagekit.DefaultContentType)
	}
}

func missingObject(t *testing.T, s Subject) {
	ctx := t.Context()

	if _, _, err := s.Storage.Get(ctx, "no-such-key.bin"); !errors.Is(err, storagekit.ErrNotFound) {
		t.Errorf("Get missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Storage.Stat(ctx, "no-such-key.bin"); !errors.Is(err, storagekit.ErrNotFound) {
		t.Errorf("Stat missing: err = %v, want ErrNotFound", err)
	}
	if err := s.Storage.Delete(ctx, "no-such-key.bin"); err != nil {
		t.Errorf("Delete missing: err = %v, want nil (idempotent)", err)
	}
}

func emptyKeyRejected(t *testing.T, s Subject) {
	ctx := t.Context()

	if err := s.Storage.Delete(ctx, ""); !errors.Is(err, storagekit.ErrDeleteFailed) {
		t.Errorf("Delete(\"\"): err = %v, want ErrDeleteFailed", err)
	}
	if _, _, err := s.Storage.Get(ctx, ""); !errors.Is(err, storagekit.ErrGetFailed) {
		t.Errorf("Get(\"\"): err = %v, want ErrGetFailed", err)
	}
	if _, err := s.Storage.Stat(ctx, ""); !errors.Is(err, storagekit.ErrStatFailed) {
		t.Errorf("Stat(\"\"): err = %v, want ErrStatFailed", err)
	}
}

func presignValidatesArgs(t *testing.T, s Subject) {
	ctx := t.Context()

	for _, ttl := range []time.Duration{0, 500 * time.Millisecond, storagekit.MaxPresignTTL + time.Second} {
		if _, err := s.Presigner.PresignGet(ctx, "obj.bin", ttl); !errors.Is(err, storagekit.ErrPresignFailed) {
			t.Errorf("PresignGet(ttl=%s): err = %v, want ErrPresignFailed", ttl, err)
		}
		if _, err := s.Presigner.PresignPut(ctx, "obj.bin", ttl); !errors.Is(err, storagekit.ErrPresignFailed) {
			t.Errorf("PresignPut(ttl=%s): err = %v, want ErrPresignFailed", ttl, err)
		}
	}
	if _, err := s.Presigner.PresignGet(ctx, "", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("PresignGet(\"\"): err = %v, want ErrPresignFailed", err)
	}
	if _, err := s.Presigner.PresignPut(ctx, "", time.Minute); !errors.Is(err, storagekit.ErrPresignFailed) {
		t.Errorf("PresignPut(\"\"): err = %v, want ErrPresignFailed", err)
	}
	for _, ttl := range []time.Duration{storagekit.MinPresignTTL, storagekit.MaxPresignTTL} {
		if _, err := s.Presigner.PresignGet(ctx, "obj.bin", ttl); err != nil {
			t.Errorf("PresignGet(ttl=%s): %v", ttl, err)
		}
	}
}

func presignGetAndPut(t *testing.T, s Subject) {
	ctx := t.Context()

	link, err := s.Presigner.PresignGet(ctx, "dir/obj.bin", 15*time.Minute,
		storagekit.WithResponseContentDisposition(`attachment; filename="report.pdf"`),
		storagekit.WithResponseContentType("application/pdf"))
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	for _, want := range []string{"dir/obj.bin", "response-content-disposition", "report.pdf", "response-content-type", "application/pdf"} {
		if !strings.Contains(link, want) && !strings.Contains(unescape(link), want) {
			t.Errorf("presigned GET %q missing %q", link, want)
		}
	}

	put, err := s.Presigner.PresignPut(ctx, "dir/obj.bin", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	if !strings.Contains(put, "obj.bin") {
		t.Errorf("presigned PUT %q missing key", put)
	}
}

func contextCancelled(t *testing.T, s Subject) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.Storage.Put(ctx, strings.NewReader("x"), "text/plain", 1); err == nil {
		t.Error("Put with cancelled ctx: want error")
	}
	if _, _, err := s.Storage.Get(ctx, "some.bin"); err == nil {
		t.Error("Get with cancelled ctx: want error")
	}
	if _, err := s.Storage.Stat(ctx, "some.bin"); err == nil {
		t.Error("Stat with cancelled ctx: want error")
	}
	if err := s.Storage.Delete(ctx, "some.bin"); err == nil {
		t.Error("Delete with cancelled ctx: want error")
	}
}

// hiddenLen прячет Seeker/ReaderAt: контракт size проверяется и на источнике,
// длину которого нельзя узнать заранее.
type hiddenLen struct{ io.Reader }

func unescape(s string) string {
	r := strings.NewReplacer("%20", " ", "%22", `"`, "%3B", ";", "%2F", "/")
	return r.Replace(s)
}
