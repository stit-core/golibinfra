package anthropickit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Гейтвей закрывает стрим из горутины отмены, пока другая читает Next:
// соединение рвётся сразу, метрики брошенного стрима пишутся ровно один раз.
// Гонку по внутреннему состоянию ловит -race.
func TestStream_CloseFromAnotherGoroutineWhileReading(t *testing.T) {
	reader, _ := installOTel(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, streamEvents[0]) // message_start: usage уже накоплен
		fl.Flush()
		<-release // держим соединение открытым, чтобы Next завис на чтении
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	c := newTestClient(t, srv.URL, nil, nil)
	st := c.Stream(context.Background(), userRequest("hi"))

	firstEvent := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for i := 0; st.Next(); i++ {
			if i == 0 {
				close(firstEvent)
			}
		}
	}()

	<-firstEvent
	// Пауза только подводит читателя к блокирующему чтению; ассерты ниже верны
	// при любом порядке — тем и проверяется контракт.
	time.Sleep(20 * time.Millisecond)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock the reader")
	}

	if u := st.Usage(); u.InputTokens != 25 {
		t.Errorf("usage after close = %+v, want the tokens accounted at message_start", u)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", nil); got != 1 {
		t.Errorf("calls_total = %d, want exactly 1 — the abandoned stream is accounted once", got)
	}
	if got := counterSum(t, reader, "anthropic_api_calls_total", map[string]string{"op": "stream", "status": "abandoned"}); got != 1 {
		t.Errorf("calls_total{op=stream,status=abandoned} = %d, want 1", got)
	}
	if got := counterSum(t, reader, "anthropic_api_tokens_total", map[string]string{"kind": "input"}); got != 25 {
		t.Errorf("tokens_total{kind=input} = %d, want 25", got)
	}
}
