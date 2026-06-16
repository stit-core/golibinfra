package yandexaikit

import (
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/clockkit"
	"git.stit.tech/stit-core/golibinfra/httpclient"
)

func testClient(t *testing.T, cfg Config, opts ...Option) *Client {
	t.Helper()
	hc := httpclient.New(httpclient.Config{})
	c, err := New(cfg, hc, clockkit.Real{}, nil, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRequiresHTTPClient(t *testing.T) {
	if _, err := New(Config{APIKey: "k"}, nil, nil, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewAPIKeyForbidsFolder(t *testing.T) {
	hc := httpclient.New(httpclient.Config{})
	_, err := New(Config{APIKey: "k", FolderID: "f"}, hc, nil, nil)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig (folder with Api-Key forbidden)", err)
	}
}

func TestNewIAMRequiresFields(t *testing.T) {
	hc := httpclient.New(httpclient.Config{})
	// IAM-схема (APIKey пуст) без folder/ключа — ошибка.
	if _, err := New(Config{}, hc, nil, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestModelURIPassthrough(t *testing.T) {
	c := testClient(t, Config{APIKey: "k"})
	if got := c.modelURI("gpt", "gpt://folder/yandexgpt/latest"); got != "gpt://folder/yandexgpt/latest" {
		t.Fatalf("modelURI passthrough = %q", got)
	}
}

func TestModelURIBuilds(t *testing.T) {
	// IAM-схема с folder — bare-id собирается в gpt://<folder>/<model>.
	hc := httpclient.New(httpclient.Config{})
	c, err := New(Config{
		FolderID:            "b1g",
		ServiceAccountID:    "sa",
		ServiceAccountKeyID: "kid",
		PrivateKey:          testRSAKeyPEM(t),
	}, hc, clockkit.Real{}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.modelURI("gpt", "yandexgpt"); got != "gpt://b1g/yandexgpt" {
		t.Fatalf("modelURI = %q, want gpt://b1g/yandexgpt", got)
	}
}

func TestResponseTextAndContentFilter(t *testing.T) {
	r := &Response{Alternatives: []Alternative{
		{Message: Message{Text: "a"}, Status: StatusPartial},
		{Message: Message{Text: "b"}, Status: StatusContentFilter},
	}}
	if got := r.Text(); got != "ab" {
		t.Fatalf("Text() = %q, want ab", got)
	}
	if !r.IsContentFilter() {
		t.Fatal("IsContentFilter() = false, want true")
	}
}

func TestValidateResponseFormat(t *testing.T) {
	if err := (Request{JSONObject: true, JSONSchema: []byte("{}")}).validateResponseFormat(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if err := (Request{JSONObject: true}).validateResponseFormat(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}
