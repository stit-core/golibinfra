package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func sseClient() *Client { c, _ := New(Config{Timeout: 2 * time.Second}); return c }

func TestSSE_ParsesEventsAndDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: hello\n\n")
		fmt.Fprint(w, "data: world\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	stream, err := sseClient().Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got []string
	for ev := range stream.Events() {
		got = append(got, ev.Data)
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	want := []string{"hello", "world", "[DONE]"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestSSE_FieldsAndMultilineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// event + id + двухстрочный data; строки-комментарии (": ping") игнорируются.
		fmt.Fprint(w, ": ping\n")
		fmt.Fprint(w, "event: token\nid: 7\ndata: line1\ndata: line2\n\n")
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	stream, err := sseClient().Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ev, ok := <-stream.Events()
	if !ok {
		t.Fatal("no event")
	}
	if ev.Event != "token" || ev.ID != "7" || ev.Data != "line1\nline2" {
		t.Fatalf("bad event: %+v", ev)
	}
	for range stream.Events() { //nolint:revive // дренаж до закрытия
	}
}

func TestSSE_ContextCancelIsClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		<-r.Context().Done() // держим открытым, пока клиент не отменит ctx (разрыв = выход)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	stream, err := sseClient().Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if ev := <-stream.Events(); ev.Data != "first" {
		t.Fatalf("first event: %+v", ev)
	}
	cancel() // barge-in

	for range stream.Events() { //nolint:revive // дренаж до закрытия
	}
	if err := stream.Err(); err != nil { // отмена ctx — норма, Err остаётся nil
		t.Fatalf("cancel should be clean, got %v", err)
	}
}

func TestSSE_Non2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := sseClient().Stream(req); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestSSE_CloseStopsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 1000; i++ {
			if r.Context().Err() != nil {
				return // клиент закрыл поток (Close)
			}
			fmt.Fprintf(w, "data: %d\n\n", i)
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	stream, err := sseClient().Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	<-stream.Events() // одно событие
	_ = stream.Close()
	for range stream.Events() { //nolint:revive // дренаж до закрытия
	}
}
