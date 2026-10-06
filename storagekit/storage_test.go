package storagekit_test

import (
	"errors"
	"mime"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/idkit"
	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func TestNewKey_ExtByContentType(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":                    ".jpg",
		"image/png":                     ".png",
		"image/webp":                    ".webp",
		"image/gif":                     ".gif",
		"image/svg+xml":                 ".svg",
		"application/pdf":               ".pdf",
		"application/json":              ".json",
		"application/zip":               ".zip",
		"audio/ogg":                     ".ogg",
		"audio/mpeg":                    ".mp3",
		"video/mp4":                     ".mp4",
		"text/plain":                    ".txt",
		"text/plain; charset=utf-8":     ".txt",
		"TEXT/PLAIN":                    ".txt",
		"application/octet-stream":      "",
		"application/x-unknown-type-42": "",
		"":                              "",
	}
	for ct, wantExt := range cases {
		key := storagekit.NewKey(ct)
		if !strings.HasSuffix(key, wantExt) {
			t.Errorf("NewKey(%q) = %q, want suffix %q", ct, key, wantExt)
		}
		if id := strings.TrimSuffix(key, wantExt); !idkit.IsV7(id) {
			t.Errorf("NewKey(%q) prefix %q is not an idkit UUIDv7", ct, id)
		}
	}
}

// Тип вне таблицы кита берёт расширение из mime-базы процесса: своя запись в
// ней делает проверку независимой от mime-базы ОС.
func TestNewKey_ExtFromMimeDatabase(t *testing.T) {
	const contentType = "application/x-stit-fixture"
	if err := mime.AddExtensionType(".stitfix", contentType); err != nil {
		t.Fatalf("AddExtensionType: %v", err)
	}

	key := storagekit.NewKey(contentType)
	if !strings.HasSuffix(key, ".stitfix") {
		t.Fatalf("NewKey(%q) = %q, want suffix %q", contentType, key, ".stitfix")
	}
}

func TestNewKey_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		k := storagekit.NewKey("image/png")
		if _, dup := seen[k]; dup {
			t.Fatalf("NewKey produced duplicate %q", k)
		}
		seen[k] = struct{}{}
	}
}

func TestNormalizeContentType(t *testing.T) {
	for in, want := range map[string]string{
		"":           storagekit.DefaultContentType,
		"   ":        storagekit.DefaultContentType,
		"image/png":  "image/png",
		"text/plain": "text/plain",
	} {
		if got := storagekit.NormalizeContentType(in); got != want {
			t.Errorf("NormalizeContentType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidatePresignTTL(t *testing.T) {
	valid := []time.Duration{storagekit.MinPresignTTL, time.Minute, storagekit.MaxPresignTTL}
	invalid := []time.Duration{-time.Second, 0, 999 * time.Millisecond, storagekit.MaxPresignTTL + time.Second}

	for _, ttl := range valid {
		if err := storagekit.ValidatePresignTTL(ttl); err != nil {
			t.Errorf("ValidatePresignTTL(%s) = %v, want nil", ttl, err)
		}
	}
	for _, ttl := range invalid {
		if err := storagekit.ValidatePresignTTL(ttl); !errors.Is(err, storagekit.ErrPresignFailed) {
			t.Errorf("ValidatePresignTTL(%s) = %v, want ErrPresignFailed", ttl, err)
		}
	}
}

func TestNewPresignParams(t *testing.T) {
	params := storagekit.NewPresignParams(
		storagekit.WithResponseContentDisposition(`attachment; filename="a.pdf"`),
		storagekit.WithResponseContentType("application/pdf"),
		nil,
	)
	if params.ContentDisposition != `attachment; filename="a.pdf"` || params.ContentType != "application/pdf" {
		t.Fatalf("params = %+v", params)
	}
	if got := storagekit.NewPresignParams(); got != (storagekit.PresignParams{}) {
		t.Fatalf("NewPresignParams() = %+v, want zero value", got)
	}
}
