package googleaikit

import (
	"errors"
	"testing"
)

func TestErrStream(t *testing.T) {
	sentinel := errors.New("boom")
	s := &errStream{err: sentinel}
	if s.Next() {
		t.Fatal("errStream.Next() = true, want false")
	}
	if !errors.Is(s.Err(), sentinel) {
		t.Fatalf("Err() = %v", s.Err())
	}
	if _, err := s.Final(); !errors.Is(err, sentinel) {
		t.Fatalf("Final() err = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestStreamMapErrorBeforeOpen(t *testing.T) {
	c := newTestClient(t)
	// Неизвестный блок ломает маппинг до открытия SSE → errStream.
	type bogus struct{ Block }
	st := c.Stream(t.Context(), Request{
		Messages: []Message{{Role: RoleUser, Content: []Block{bogus{}}}},
	})
	if st.Next() {
		t.Fatal("expected no events on mapping error")
	}
	if !errors.Is(st.Err(), ErrInvalidRequest) {
		t.Fatalf("Err() = %v, want ErrInvalidRequest", st.Err())
	}
}
