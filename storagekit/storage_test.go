package storagekit_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"git.stit.tech/stit-infra/golibinfra/storagekit"
)

func TestNewKey_ExtByContentType(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":               ".jpg",
		"image/png":                ".png",
		"image/webp":               ".webp",
		"application/pdf":          ".pdf",
		"application/octet-stream": "",
		"":                         "",
	}
	for ct, wantExt := range cases {
		key, err := storagekit.NewKey(ct)
		if err != nil {
			t.Fatalf("NewKey(%q): unexpected error: %v", ct, err)
		}
		if !strings.HasSuffix(key, wantExt) {
			t.Errorf("NewKey(%q) = %q, want suffix %q", ct, key, wantExt)
		}
		id := strings.TrimSuffix(key, wantExt)
		if _, perr := uuid.Parse(id); perr != nil {
			t.Errorf("NewKey(%q) prefix %q is not a uuid: %v", ct, id, perr)
		}
	}
}

func TestNewKey_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		k, err := storagekit.NewKey("image/png")
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		if _, dup := seen[k]; dup {
			t.Fatalf("NewKey produced duplicate %q", k)
		}
		seen[k] = struct{}{}
	}
}
