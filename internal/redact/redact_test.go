package redact

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestURL(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"no secrets", "nats://localhost:4222", "nats://localhost:4222"},
		{"user and password", "nats://user:s3cret@nats:4222", "nats://xxxxx@nats:4222"},
		{"token as user", "nats://t0ken@nats:4222", "nats://xxxxx@nats:4222"},
		{"password with at", "nats://user:p@ss@nats:4222", "nats://xxxxx@nats:4222"},
		{"password with slash", "redis://:pa/ss@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"bad percent-escape", "redis://:s3cr%zz@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"bad escape and query", "postgres://u:s3%zz@db/app?password=s3cret#frag", "postgres://xxxxx@db/app?password=xxxxx"},
		{"no scheme", "user:s3cret@nats:4222", "xxxxx@nats:4222"},
		{"no scheme, no userinfo", "localhost:4222", "localhost:4222"},
		{"query values", "https://api.example.com/v1/items?access_token=s3cret&page=2", "https://api.example.com/v1/items?access_token=xxxxx&page=xxxxx"},
		{"bare query parameter", "https://api.example.com/hook?s3cret&a=1", "https://api.example.com/hook?xxxxx&a=xxxxx"},
		{"fragment", "https://app.example.com/cb#access_token=s3cret", "https://app.example.com/cb"},
		{"at in path stays", "https://social.example.com/@alice?x=1", "https://social.example.com/@alice?x=xxxxx"},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := URL(c.raw)
			if got != c.want {
				t.Fatalf("URL(%q) = %q, want %q", c.raw, got, c.want)
			}
			if strings.Contains(got, "s3cr") || strings.Contains(got, "t0ken") {
				t.Fatalf("URL(%q) = %q keeps a secret", c.raw, got)
			}
		})
	}
}

func TestParseError(t *testing.T) {
	malformed := []string{"redis://:s3cr%zz@cache:6379", "nats://user:s3cret/x@nats"}
	_, escape := url.Parse(malformed[0])
	_, badPort := url.Parse(malformed[1])
	_, port := url.Parse("nats://user:s3cret@nats:4222/x")
	plain := errors.New("redis: invalid URL scheme: http")
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"escape", escape, "invalid percent-escape in URL"},
		{"port", badPort, "malformed URL"},
		{"other error", plain, plain.Error()},
	} {
		if got := ParseError(c.err); got.Error() != c.want || strings.Contains(got.Error(), "s3cr") {
			t.Errorf("%s: ParseError(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
	if port != nil {
		t.Fatalf("control: a valid URL must parse: %v", port)
	}
}
