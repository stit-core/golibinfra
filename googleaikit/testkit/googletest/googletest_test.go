package googletest

import (
	"context"
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/googleaikit"
)

func TestStubDefaults(t *testing.T) {
	var s Stub
	resp, err := s.GenerateContent(t.Context(), googleaikit.Request{})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if resp.Text() != "stub" {
		t.Fatalf("Text() = %q", resp.Text())
	}

	emb, err := s.EmbedContent(t.Context(), googleaikit.EmbedRequest{Texts: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("EmbedContent: %v", err)
	}
	if len(emb.Embeddings) != 2 {
		t.Fatalf("embeddings = %d, want 2", len(emb.Embeddings))
	}

	op, err := s.PollVideo(t.Context(), &googleaikit.VideoOperation{})
	if err != nil {
		t.Fatalf("PollVideo: %v", err)
	}
	if !op.Done || len(op.Videos) != 1 {
		t.Fatalf("video op = %+v", op)
	}
}

func TestStubOverride(t *testing.T) {
	want := errors.New("denied")
	s := Stub{GenerateContentFunc: func(context.Context, googleaikit.Request) (*googleaikit.Response, error) {
		return nil, want
	}}
	if _, err := s.GenerateContent(t.Context(), googleaikit.Request{}); !errors.Is(err, want) {
		t.Fatalf("override err = %v, want %v", err, want)
	}
}

func TestScriptStream(t *testing.T) {
	final := &googleaikit.Response{Content: []googleaikit.Block{googleaikit.TextBlock{Text: "done"}}}
	st := ScriptStream(
		final,
		googleaikit.StreamEvent{Type: googleaikit.EventTextDelta, Text: "he"},
		googleaikit.StreamEvent{Type: googleaikit.EventTextDelta, Text: "llo"},
		googleaikit.StreamEvent{Type: googleaikit.EventDone},
	)
	var got string
	for st.Next() {
		got += st.Event().Text
	}
	if got != "hello" {
		t.Fatalf("concatenated = %q, want hello", got)
	}
	if st.Err() != nil {
		t.Fatalf("Err() = %v", st.Err())
	}
	fin, err := st.Final()
	if err != nil || fin.Text() != "done" {
		t.Fatalf("Final = %v, %v", fin, err)
	}
}

func TestErrStream(t *testing.T) {
	sentinel := errors.New("boom")
	st := ErrStream(sentinel)
	if st.Next() {
		t.Fatal("Next() = true, want false")
	}
	if !errors.Is(st.Err(), sentinel) {
		t.Fatalf("Err() = %v", st.Err())
	}
}
