package storagekit_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func TestURLBuilder_Build(t *testing.T) {
	cases := []struct {
		name                     string
		base, bucket, style, key string
		want                     string
	}{
		{
			name: "path style", base: "https://cdn.example.com", bucket: "media",
			style: storagekit.URLStylePath, key: "abc.jpg", want: "https://cdn.example.com/media/abc.jpg",
		},
		{
			name: "trailing slashes collapse", base: "https://cdn.example.com///", bucket: "m",
			style: storagekit.URLStylePath, key: "k.pdf", want: "https://cdn.example.com/m/k.pdf",
		},
		{
			name: "empty style is path", base: "https://cdn.example.com/", bucket: "media",
			key: "abc.jpg", want: "https://cdn.example.com/media/abc.jpg",
		},
		{
			name: "virtual style omits bucket", base: "https://media.cdn.example.com", bucket: "media",
			style: storagekit.URLStyleVirtual, key: "abc.jpg", want: "https://media.cdn.example.com/abc.jpg",
		},
		{
			name: "nested key keeps separators", base: "https://cdn.example.com", bucket: "media",
			style: storagekit.URLStylePath, key: "2026/07/abc.jpg", want: "https://cdn.example.com/media/2026/07/abc.jpg",
		},
		{
			name: "unsafe characters are escaped", base: "https://cdn.example.com", bucket: "media",
			style: storagekit.URLStylePath, key: "a b#c?d.txt", want: "https://cdn.example.com/media/a%20b%23c%3Fd.txt",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := storagekit.NewURLBuilder(c.base, c.bucket, c.style).Build(c.key)
			if got != c.want {
				t.Errorf("Build(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

// Nil-приёмник — путь zero value MemoryStorage: Ref.URL остаётся ключом.
func TestURLBuilder_BuildNilReceiver(t *testing.T) {
	var b *storagekit.URLBuilder
	if got := b.Build("a b.txt"); got != "a%20b.txt" {
		t.Fatalf("Build on nil builder = %q, want %q", got, "a%20b.txt")
	}
}
