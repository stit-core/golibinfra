package httpkit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseWriter_WrapIsIdempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	a := wrap(rec)
	b := wrap(a)
	if a != b {
		t.Fatal("wrapping a wrapper must return the same wrapper")
	}
}

func TestResponseWriter_StatusBytesAndDoubleWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	w := wrap(rec)
	w.WriteHeader(http.StatusCreated)
	w.WriteHeader(http.StatusOK) // игнорируется: заголовки уже отправлены
	n, _ := w.Write([]byte("hello"))
	if w.status != http.StatusCreated || w.bytes != int64(n) || rec.Code != http.StatusCreated {
		t.Fatalf("status=%d bytes=%d rec=%d", w.status, w.bytes, rec.Code)
	}
}

func TestResponseWriter_ImplicitOK(t *testing.T) {
	rec := httptest.NewRecorder()
	w := wrap(rec)
	_, _ = w.Write([]byte("x"))
	if w.status != http.StatusOK || !w.wroteHeader {
		t.Fatalf("Write without WriteHeader must record 200: %d", w.status)
	}
}

func TestResponseWriter_HijackWithoutHijacker(t *testing.T) {
	w := wrap(httptest.NewRecorder())
	if _, _, err := w.Hijack(); err == nil {
		t.Fatal("recorder is not a Hijacker; error expected")
	}
}
