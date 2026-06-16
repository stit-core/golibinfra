package sberaikit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/clockkit"
)

// newTestClient собирает Client против httptest-сервера. Scoped-TLS транспорт
// подменяется на server.Client() (WithHTTPClient), OAuth/Base URL — на адрес
// сервера. Так тесты идут без сети и без реального Минцифры-CA.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	cfg := Config{
		AuthKey:      "dGVzdC1rZXk=",
		Scope:        "GIGACHAT_API_PERS",
		BaseURL:      srv.URL + "/api/v1",
		OAuthURL:     srv.URL + "/api/v2/oauth",
		DefaultModel: "GigaChat-2",
		MaxRetries:   2,
		Timeout:      5 * time.Second,
	}
	c, err := New(cfg, nil, clockkit.Real{}, WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// oauthHandler — общий обработчик OAuth для тест-сервера; проверяет Basic+RqUID.
func oauthHandler(t *testing.T) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			t.Errorf("oauth: missing Basic auth")
		}
		if r.Header.Get("RqUID") == "" {
			t.Errorf("oauth: missing RqUID")
		}
		exp := time.Now().Add(30 * time.Minute).UnixMilli()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: "test-access-token", ExpiresAt: exp})
	}
}

func TestChat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			t.Errorf("chat: bad bearer %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("RqUID") == "" {
			t.Errorf("chat: missing RqUID")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"object":"chat.completion","model":"GigaChat-2",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Привет"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13,"precached_prompt_tokens":2}
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "привет"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text() != "Привет" {
		t.Fatalf("Text() = %q", resp.Text())
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 3 || resp.Usage.CachedTokens != 2 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	// 13 total @ 65_000_000/MTok = 13*65 = 845 micro-RUB.
	if resp.Cost != 845 {
		t.Fatalf("cost = %d, want 845", resp.Cost)
	}
}

func TestChatErrorClassified(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"status":400,"message":"bad model"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "bad model" {
		t.Fatalf("APIError not surfaced: %v", err)
	}
}

func TestImageAsFile(t *testing.T) {
	const fileID = "a598ae3d-d3eb-454e-a8bf-0848193a603e"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"object":"chat.completion","model":"GigaChat-2",
			"choices":[{"index":0,"message":{"role":"assistant","content":"<img src=\"`+fileID+`\" fuse=\"true\"/>"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":40,"total_tokens":60}
		}`)
	})
	mux.HandleFunc("/api/v1/files/"+fileID+"/content", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/jpg" {
			t.Errorf("file: Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF}) // JPEG magic
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	res, err := c.GenerateImage(context.Background(), "GigaChat-2", "Нарисуй розового кота")
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(res.Images))
	}
	img := res.Images[0]
	if img.FileID != fileID {
		t.Fatalf("file id = %q", img.FileID)
	}
	if len(img.Bytes) != 3 || img.Bytes[0] != 0xFF {
		t.Fatalf("image bytes = %v", img.Bytes)
	}
	if img.ContentType != "image/jpeg" {
		t.Fatalf("content type = %q", img.ContentType)
	}
}

func TestEmbeddings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"object":"list","model":"Embeddings",
			"data":[
				{"object":"embedding","embedding":[0.1,0.2],"index":0,"usage":{"prompt_tokens":4}},
				{"object":"embedding","embedding":[0.3,0.4],"index":1,"usage":{"prompt_tokens":6}}
			]
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	resp, err := c.Embeddings(context.Background(), EmbeddingsRequest{Model: "Embeddings", Input: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data = %d", len(resp.Data))
	}
	if resp.Usage.InputTokens != 10 { // 4 + 6
		t.Fatalf("usage InputTokens = %d, want 10", resp.Usage.InputTokens)
	}
	// 10 @ 14_000_000/MTok = 140 micro-RUB.
	if resp.Cost != 140 {
		t.Fatalf("cost = %d, want 140", resp.Cost)
	}
}

func TestImageFileIDsParse(t *testing.T) {
	r := &ChatResponse{Choices: []Choice{{Message: Message{
		Content: `тут <img src="id-1" fuse="true"/> и <img src="id-2"/>`,
	}}}}
	ids := r.ImageFileIDs()
	if len(ids) != 2 || ids[0] != "id-1" || ids[1] != "id-2" {
		t.Fatalf("ids = %v", ids)
	}
}
