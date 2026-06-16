package yandextest

import (
	"context"
	"errors"
	"testing"

	"git.stit.tech/stit-core/golibinfra/yandexaikit"
)

func TestStubDefaults(t *testing.T) {
	var s Stub
	resp, err := s.Completion(context.Background(), yandexaikit.Request{})
	if err != nil || resp.Text() != "stub" {
		t.Fatalf("Completion = %+v err=%v", resp, err)
	}
	id, err := s.CompletionAsync(context.Background(), yandexaikit.Request{})
	if err != nil || id != "op-stub" {
		t.Fatalf("CompletionAsync id=%q err=%v", id, err)
	}
}

func TestStubStream(t *testing.T) {
	var s Stub
	st := s.Stream(context.Background(), yandexaikit.Request{})
	var n int
	for st.Next() {
		n++
	}
	if n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	final, err := st.Final()
	if err != nil || final.Text() != "stub" {
		t.Fatalf("final = %+v err=%v", final, err)
	}
}

func TestErrStream(t *testing.T) {
	sentinel := errors.New("boom")
	st := ErrStream(sentinel)
	if st.Next() {
		t.Fatal("Next() should be false on ErrStream")
	}
	if !errors.Is(st.Err(), sentinel) {
		t.Fatalf("Err() = %v", st.Err())
	}
}
