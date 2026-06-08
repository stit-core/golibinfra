package storagekit_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/storagekit"
)

func TestURLBuilder_Build(t *testing.T) {
	cases := []struct {
		base, bucket, key, want string
	}{
		{"https://cdn.example.com", "media", "abc.jpg", "https://cdn.example.com/media/abc.jpg"},
		{"https://cdn.example.com/", "media", "abc.jpg", "https://cdn.example.com/media/abc.jpg"},
		{"https://cdn.example.com///", "m", "k.pdf", "https://cdn.example.com/m/k.pdf"},
	}
	for _, c := range cases {
		got := storagekit.NewURLBuilder(c.base, c.bucket).Build(c.key)
		if got != c.want {
			t.Errorf("Build(base=%q,bucket=%q,key=%q) = %q, want %q", c.base, c.bucket, c.key, got, c.want)
		}
	}
}
