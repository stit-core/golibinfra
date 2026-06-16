package openaitest

import (
	"context"
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/openaikit"
)

func TestStubDefaults(t *testing.T) {
	var s Stub
	resp, err := s.Chat(context.Background(), openaikit.ChatRequest{})
	if err != nil || resp.Text != "stub" {
		t.Fatalf("Chat default = %v, %v", resp, err)
	}
	if _, err := s.Respond(context.Background(), openaikit.ResponsesRequest{}); err != nil {
		t.Fatalf("Respond default: %v", err)
	}
}

func TestStubOverride(t *testing.T) {
	s := Stub{
		ChatFunc: func(_ context.Context, _ openaikit.ChatRequest) (*openaikit.ChatResponse, error) {
			return &openaikit.ChatResponse{Text: "custom"}, nil
		},
	}
	resp, _ := s.Chat(context.Background(), openaikit.ChatRequest{})
	if resp.Text != "custom" {
		t.Fatalf("override text = %q", resp.Text)
	}
}

func TestScriptStream(t *testing.T) {
	final := &openaikit.ChatResponse{Text: "done"}
	st := ScriptStream(
		final,
		openaikit.StreamEvent{Type: openaikit.EventTextDelta, Text: "a"},
		openaikit.StreamEvent{Type: openaikit.EventDone},
	)
	var n int
	for st.Next() {
		n++
	}
	if n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	got, err := st.Final()
	if err != nil || got.Text != "done" {
		t.Fatalf("Final = %v, %v", got, err)
	}
}

func TestErrStream(t *testing.T) {
	sentinel := errors.New("boom")
	st := ErrStream(sentinel)
	if st.Next() {
		t.Fatal("ErrStream.Next should be false")
	}
	if !errors.Is(st.Err(), sentinel) {
		t.Fatalf("Err = %v", st.Err())
	}
	if _, err := st.Final(); !errors.Is(err, sentinel) {
		t.Fatalf("Final err = %v", err)
	}
}
