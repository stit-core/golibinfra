package openaikit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestNewRequiresAPIKey(t *testing.T) {
	if _, err := New(Config{}, nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestNewWithKey(t *testing.T) {
	c, err := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.DefaultModel != "gpt-5.5" {
		t.Fatalf("default model not set: %q", c.cfg.DefaultModel)
	}
}

func TestModelOrFallsBackToDefault(t *testing.T) {
	c, _ := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil)
	if got := c.modelOr(""); got != "gpt-5.5" {
		t.Fatalf("modelOr(\"\") = %q, want gpt-5.5", got)
	}
	if got := c.modelOr("gpt-4.1"); got != "gpt-4.1" {
		t.Fatalf("modelOr override = %q", got)
	}
}

func TestToChatParamsStreamSetsIncludeUsage(t *testing.T) {
	c, _ := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil)
	params, model := c.toChatParams(ChatRequest{
		Messages: []ChatMessage{{Role: RoleUser, Text: "hi"}},
	}, true)
	if model != "gpt-5.5" {
		t.Fatalf("model = %q", model)
	}
	if !params.StreamOptions.IncludeUsage.Value {
		t.Fatal("include_usage not set for streaming chat")
	}
}

func TestToResponsesParamsJSONSchema(t *testing.T) {
	c, _ := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil)
	params, _ := c.toResponsesParams(ResponsesRequest{
		Input: "hi",
		JSONSchema: &JSONSchema{
			Name:   "out",
			Schema: map[string]any{"type": "object"},
			Strict: true,
		},
	})
	if params.Text.Format.OfJSONSchema == nil {
		t.Fatal("JSONSchema не пробросился в Text.Format")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{400, ErrInvalidRequest},
		{401, ErrAuth},
		{403, ErrPermission},
		{404, ErrNotFound},
		{409, ErrConflict},
		{429, ErrRateLimited},
		{500, ErrServer},
	}
	for _, tc := range cases {
		err := classify(&openai.Error{StatusCode: tc.status})
		if !errors.Is(err, tc.want) {
			t.Fatalf("classify(%d) = %v, want %v", tc.status, err, tc.want)
		}
	}
	if classify(nil) != nil {
		t.Fatal("classify(nil) should be nil")
	}
	plain := errors.New("boom")
	if !errors.Is(classify(plain), plain) {
		t.Fatal("non-SDK error should pass through")
	}
}

// TestChatRawRoundTrip — синхронный Chat против httptest-сервера (без сети):
// нормализованный текст + usage + cost через прайс-таблицу.
func TestChatRawRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","model":"gpt-5.5","object":"chat.completion",
			"choices":[{"index":0,"finish_reason":"stop",
				"message":{"role":"assistant","content":"hello"}}],
			"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}
		}`))
	}))
	defer srv.Close()

	c, err := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text != "hello" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Usage.InputTokens != 1_000_000 || resp.Usage.OutputTokens != 1_000_000 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	// gpt-5.5: 1M in @ $5 + 1M out @ $30 = $35.
	if resp.Cost != 35_000_000 {
		t.Fatalf("cost = %d, want 35000000", resp.Cost)
	}
}

// TestChatRawRoundTripError — 401 мапится в ErrAuth.
func TestChatRawRoundTripError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"authentication_error"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{APIKey: "sk-bad", DefaultModel: "gpt-5.5", MaxRetries: 0}, nil, WithBaseURL(srv.URL))
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: RoleUser, Text: "hi"}},
	})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// TestRespondRawRoundTrip — синхронный Responses против httptest-сервера.
func TestRespondRawRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp-1","object":"response","model":"gpt-5.5","status":"completed",
			"output":[{"type":"message","role":"assistant","id":"m1","status":"completed",
				"content":[{"type":"output_text","text":"hi there","annotations":[]}]}],
			"usage":{"input_tokens":1000000,"output_tokens":0,"total_tokens":1000000,
				"input_tokens_details":{"cached_tokens":0},
				"output_tokens_details":{"reasoning_tokens":0}}
		}`))
	}))
	defer srv.Close()

	c, _ := New(Config{APIKey: "sk-test", DefaultModel: "gpt-5.5"}, nil, WithBaseURL(srv.URL))
	resp, err := c.Respond(context.Background(), ResponsesRequest{Input: "hi"})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if resp.Text != "hi there" {
		t.Fatalf("text = %q", resp.Text)
	}
	// gpt-5.5: 1M in @ $5 = $5.
	if resp.Cost != 5_000_000 {
		t.Fatalf("cost = %d, want 5000000", resp.Cost)
	}
}
