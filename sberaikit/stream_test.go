package sberaikit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatStream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"object":"chat.completion.chunk","model":"GigaChat-2","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`data: {"object":"chat.completion.chunk","model":"GigaChat-2","choices":[{"index":0,"delta":{"content":"При"},"finish_reason":null}]}`,
			`data: {"object":"chat.completion.chunk","model":"GigaChat-2","choices":[{"index":0,"delta":{"content":"вет"},"finish_reason":"stop"}]}`,
			`data: {"object":"chat.completion.chunk","model":"GigaChat-2","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"precached_prompt_tokens":1}}`,
			`data: [DONE]`,
		}
		for _, ch := range chunks {
			_, _ = io.WriteString(w, ch+"\n\n")
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	st := c.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "привет"}}})
	defer st.Close()

	var text string
	var lastFinish FinishReason
	for st.Next() {
		ev := st.Event()
		text += ev.Content
		if ev.FinishReason != "" {
			lastFinish = ev.FinishReason
		}
	}
	if err := st.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if text != "Привет" {
		t.Fatalf("streamed text = %q, want Привет", text)
	}
	if lastFinish != FinishStop {
		t.Fatalf("finish = %q", lastFinish)
	}

	final, err := st.Final()
	if err != nil {
		t.Fatalf("Final: %v", err)
	}
	if final.Text() != "Привет" {
		t.Fatalf("final text = %q", final.Text())
	}
	if final.Usage.InputTokens != 5 || final.Usage.OutputTokens != 2 || final.Usage.CachedTokens != 1 {
		t.Fatalf("final usage = %+v", final.Usage)
	}
	// 7 total @ 65_000_000/MTok = 7*65 = 455 micro-RUB.
	if final.Cost != 455 {
		t.Fatalf("final cost = %d, want 455", final.Cost)
	}
}

func TestChatStreamStopsOnDone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth", oauthHandler(t))
	mux.HandleFunc("/api/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		// Контент после [DONE] не должен читаться.
		_, _ = io.WriteString(w, `data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"LEAK"}}]}`+"\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	st := c.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	defer st.Close()

	var text string
	for st.Next() {
		text += st.Event().Content
	}
	if text != "x" {
		t.Fatalf("text = %q, want x (no leak past [DONE])", text)
	}
}
