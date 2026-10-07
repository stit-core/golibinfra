package redact

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// noSecret — в выводе нет ни одного куска пароля: маркер секрета форм — "PWx" и "s3cr".
func noSecret(t *testing.T, fn, raw, got string) {
	t.Helper()
	for _, part := range []string{"PWx", "s3cr", "t0ken", "secret"} {
		if strings.Contains(got, part) {
			t.Fatalf("%s(%q) = %q keeps a secret", fn, raw, got)
		}
	}
}

// Пароль конфига с неэкранированным спецсимволом разбор читает как порт, путь, query или фрагмент:
// маска — всё до последнего `@`, без разбора (B1 валидатора C).
func TestURL(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"no secrets", "nats://localhost:4222", "nats://localhost:4222"},
		{"user and password", "nats://user:s3cret@nats:4222", "nats://xxxxx@nats:4222"},
		{"token as user", "nats://t0ken@nats:4222", "nats://xxxxx@nats:4222"},
		{"password with at", "nats://user:p@ss@nats:4222", "nats://xxxxx@nats:4222"},
		{"password with slash", "redis://:pa/ss@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"digits and slash parse as port and path", "redis://u:12/PWxsecret@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"password with hash", "redis://u:PWx#secret@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"digits and hash parse as port and fragment", "redis://u:123#PWx@cache:6379", "redis://xxxxx@cache:6379"},
		{"password with question mark", "https://u:PWx?secret@s3.example.com", "https://xxxxx@s3.example.com"},
		{"digits and question mark parse as port and query", "https://u:123?PWx@collector:4318", "https://xxxxx@collector:4318"},
		{"bad percent-escape", "redis://:s3cr%zz@cache:6379/0", "redis://xxxxx@cache:6379/0"},
		{"bad escape and query", "postgres://u:s3%zz@db/app?password=s3cret#frag", "postgres://xxxxx@db/app?password=xxxxx"},
		{"no scheme", "user:s3cret@nats:4222", "xxxxx@nats:4222"},
		{"no scheme, hash in password", "u:PWx#secret@minio:9000", "xxxxx@minio:9000"},
		{"no scheme, no userinfo", "localhost:4222", "localhost:4222"},
		{"query values", "https://api.example.com/v1/items?access_token=s3cret&page=2", "https://api.example.com/v1/items?access_token=xxxxx&page=xxxxx"},
		{"bare query parameter", "https://api.example.com/hook?s3cret&a=1", "https://api.example.com/hook?xxxxx&a=xxxxx"},
		{"fragment", "https://app.example.com/cb#access_token=s3cret", "https://app.example.com/cb"},
		{"empty", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := URL(c.raw)
			if got != c.want {
				t.Fatalf("URL(%q) = %q, want %q", c.raw, got, c.want)
			}
			noSecret(t, "URL", c.raw, got)
		})
	}
}

// URL запроса: хост остаётся; userinfo, который узнал разбор, путь до последнего `@`, query и
// фрагмент — маска; неразбираемый URL и `@` во фрагменте — строгая маска URL.
func TestRequestURL(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"no secrets", "https://api.example.com/v1/items", "https://api.example.com/v1/items"},
		{"user and password", "https://user:s3cret@api.example.com/x", "https://xxxxx@api.example.com/x"},
		{"password with at", "https://user:p@ss@api.example.com/x", "https://xxxxx@api.example.com/x"},
		{"handle in path", "https://social.example.com/@alice?x=1", "https://social.example.com/xxxxx@alice?x=xxxxx"},
		{"email in path", "https://www.googleapis.com/calendar/v3/calendars/a@b.com/events", "https://www.googleapis.com/xxxxx@b.com/events"},
		{"digits and slash parse as port and path", "http://u:12/PWxsecret@127.0.0.1:1/x", "http://u:12/xxxxx@127.0.0.1:1/x"},
		{"digits and slashes", "http://u:12/ab/PWx@127.0.0.1:1/x", "http://u:12/xxxxx@127.0.0.1:1/x"},
		{"email in query is masked", "https://api.example.com/users?email=a@b.com", "https://api.example.com/users?email=xxxxx"},
		{"query values", "https://api.example.com/hook?access_token=s3cret", "https://api.example.com/hook?access_token=xxxxx"},
		{"fragment", "https://app.example.com/cb#access_token=s3cret", "https://app.example.com/cb"},
		{"unparsable: hash in password", "http://u:PWx#secret@127.0.0.1:1/x", "http://xxxxx@127.0.0.1:1/x"},
		{"unparsable: question mark in password", "http://u:PWx?secret@127.0.0.1:1/x", "http://xxxxx@127.0.0.1:1/x"},
		{"unparsable: bad escape", "http://u:PWx%zzsecret@127.0.0.1:1/x", "http://xxxxx@127.0.0.1:1/x"},
		{"at in fragment: digits and hash", "http://u:123#PWx@127.0.0.1:1/x", "http://xxxxx@127.0.0.1:1/x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := RequestURL(c.raw)
			if got != c.want {
				t.Fatalf("RequestURL(%q) = %q, want %q", c.raw, got, c.want)
			}
			noSecret(t, "RequestURL", c.raw, got)
		})
	}
}

// `@` пути — хвост пароля с `/`, только если он не экранирован: RawPath разбор хранит, когда
// исходный путь отличается от канонического (`%40`).
func TestAtInPath(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://u:12/PWxsecret@cdn.example.com":    true,
		"https://cdn.example.com/u/me@x.com":        true,
		"https://cdn.example.com/u/me%40x.com":      false,
		"https://cdn.example.com/a%2Fb/me%40x.com":  false,
		"https://cdn.example.com/a%2Fb/me@x.com":    true,
		"https://collector.example.com/otlp":        false,
		"https://user:p@ss@collector.example.com/x": false,
		"https://cdn.example.com/u?email=me@x.com":  false,
		"nats://app:12/PWxsecret@127.0.0.1:1":       true,
		"nats://app:PWx%2Fsecret@127.0.0.1:1/x%40y": false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		if got := AtInPath(u); got != want {
			t.Errorf("AtInPath(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestParseError(t *testing.T) {
	malformed := []string{"redis://:s3cr%zz@cache:6379", "nats://user:s3cret/x@nats"}
	_, escape := url.Parse(malformed[0])
	_, badPort := url.Parse(malformed[1])
	_, port := url.Parse("nats://user:s3cret@nats:4222/x")
	goRedis := errors.New("redis: invalid URL path: /PWxsecret@cache:6379/0")
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"escape", escape, "invalid percent-escape in URL"},
		{"port", badPort, "malformed URL"},
		{"go-redis error quotes the path", goRedis, "malformed URL"},
	} {
		if got := ParseError(c.err); got.Error() != c.want {
			t.Errorf("%s: ParseError(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
	if port != nil {
		t.Fatalf("control: a valid URL must parse: %v", port)
	}
}
