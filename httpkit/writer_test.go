package httpkit

import (
	"errors"
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

// Регрессия: WebSocket-upgrade проходит сквозь всю цепочку — Hijack и Unwrap
// должны доходить до реального соединения.
func TestResponseWriter_HijackAndUnwrapReachConn(t *testing.T) {
	hijacked := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ww := wrap(wrap(w))
		if ww.Unwrap() == nil {
			hijacked <- errors.New("Unwrap must expose the underlying writer")
			return
		}
		conn, buf, err := ww.Hijack()
		if err != nil {
			hijacked <- err
			return
		}
		defer conn.Close()
		_, err = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		if err == nil {
			err = buf.Flush()
		}
		hijacked <- err
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := <-hijacked; err != nil {
		t.Fatalf("hijack through the wrapper: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
