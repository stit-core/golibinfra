package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func sseClient(t *testing.T) *Client {
	t.Helper()
	return newTestClient(t, Config{Timeout: 2 * time.Second})
}

// sseServer отдаёт готовый поток целиком и закрывает соединение.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openStream(t *testing.T, c *Client, url string) *SSEStream {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	stream, err := c.Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func collect(stream *SSEStream) []Event {
	var got []Event
	for ev := range stream.Events() {
		got = append(got, ev)
	}
	return got
}

func TestScanEventLines(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		atEOF   bool
		advance int
		token   string
	}{
		{name: "lf", data: "a\nb", advance: 2, token: "a"},
		{name: "crlf", data: "a\r\nb", advance: 3, token: "a"},
		{name: "cr", data: "a\rb", advance: 2, token: "a"},
		{name: "cr at buffer edge waits", data: "a\r", advance: 0},
		{name: "cr at eof", data: "a\r", atEOF: true, advance: 2, token: "a"},
		{name: "no terminator waits", data: "abc", advance: 0},
		{name: "tail at eof", data: "abc", atEOF: true, advance: 3, token: "abc"},
		{name: "empty at eof", data: "", atEOF: true, advance: 0},
		{name: "blank line", data: "\n", advance: 1, token: ""},
	}
	for _, tc := range cases {
		advance, token, err := scanEventLines([]byte(tc.data), tc.atEOF)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if advance != tc.advance || string(token) != tc.token {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", tc.name, advance, token, tc.advance, tc.token)
		}
	}
}

func TestSSE_ParsesEventsAndDone(t *testing.T) {
	srv := sseServer(t, "data: hello\n\ndata: world\n\ndata: [DONE]\n\n")

	stream := openStream(t, sseClient(t), srv.URL)
	got := collect(stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	want := []string{"hello", "world", "[DONE]"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i].Data != want[i] {
			t.Fatalf("event %d: got %q want %q", i, got[i].Data, want[i])
		}
	}
}

func TestSSE_FieldsAndMultilineData(t *testing.T) {
	srv := sseServer(t, ": ping\nevent: token\nid: 7\ndata: line1\ndata: line2\n\n")

	stream := openStream(t, sseClient(t), srv.URL)
	got := collect(stream)
	if len(got) != 1 {
		t.Fatalf("events: %+v", got)
	}
	if ev := got[0]; ev.Event != "token" || ev.ID != "7" || ev.Data != "line1\nline2" {
		t.Fatalf("bad event: %+v", ev)
	}
	if stream.LastEventID() != "7" {
		t.Errorf("LastEventID: got %q", stream.LastEventID())
	}
}

func TestSSE_EventTypeDoesNotLeakToNextEvent(t *testing.T) {
	srv := sseServer(t, "event: foo\n\ndata: bar\n\n")

	got := collect(openStream(t, sseClient(t), srv.URL))
	if len(got) != 1 {
		t.Fatalf("block without data must not be dispatched: %+v", got)
	}
	if got[0].Event != "" || got[0].Data != "bar" {
		t.Fatalf("event type leaked: %+v", got[0])
	}
}

func TestSSE_IDPersistsBetweenEvents(t *testing.T) {
	srv := sseServer(t, "id: 1\ndata: a\n\ndata: b\n\nid: 2\ndata: c\n\n")

	got := collect(openStream(t, sseClient(t), srv.URL))
	want := []Event{{ID: "1", Data: "a"}, {ID: "1", Data: "b"}, {ID: "2", Data: "c"}}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSSE_RetryFieldParsed(t *testing.T) {
	srv := sseServer(t, "retry: 1500\ndata: a\n\nretry: nope\ndata: b\n\n")

	stream := openStream(t, sseClient(t), srv.URL)
	collect(stream)
	if got := stream.Retry(); got != 1500*time.Millisecond {
		t.Fatalf("Retry: got %v, want 1.5s", got)
	}
}

func TestSSE_LineTerminators(t *testing.T) {
	// Одиночный CR — валидный терминатор строки по спеке, как и CRLF.
	srv := sseServer(t, "data: cr\r\rdata: crlf\r\n\r\ndata: lf\n\n")

	got := collect(openStream(t, sseClient(t), srv.URL))
	want := []string{"cr", "crlf", "lf"}
	if len(got) != len(want) {
		t.Fatalf("events: %+v", got)
	}
	for i := range want {
		if got[i].Data != want[i] {
			t.Errorf("event %d: got %q, want %q", i, got[i].Data, want[i])
		}
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
	stream, err := sseClient(t).Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if ev := <-stream.Events(); ev.Data != "first" {
		t.Fatalf("first event: %+v", ev)
	}
	cancel() // barge-in

	collect(stream)
	if err := stream.Err(); err != nil { // отмена ctx — норма, Err остаётся nil
		t.Fatalf("cancel should be clean, got %v", err)
	}
}

func TestSSE_MidStreamErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "1024") // обещаем больше, чем пишем
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		panic(http.ErrAbortHandler) // рвём соединение посреди тела
	}))
	defer srv.Close()

	stream := openStream(t, sseClient(t), srv.URL)
	if ev := <-stream.Events(); ev.Data != "first" {
		t.Fatalf("first event: %+v", ev)
	}
	collect(stream)
	if stream.Err() == nil {
		t.Fatal("broken stream must surface an error in Err()")
	}
}

func TestSSE_Non2xxReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := sseClient(t).Stream(req)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want *StatusError, got %v", err)
	}
	if se.Status != http.StatusNotFound || !strings.Contains(se.Body, "model not found") {
		t.Fatalf("StatusError: %+v", se)
	}
}

func TestSSE_WrongContentTypeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html>maintenance</html>")
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := sseClient(t).Stream(req); !errors.Is(err, ErrNotEventStream) {
		t.Fatalf("want ErrNotEventStream, got %v", err)
	}
}

func TestSSE_ContentTypeWithCharsetAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprint(w, "data: ok\n\n")
	}))
	defer srv.Close()

	got := collect(openStream(t, sseClient(t), srv.URL))
	if len(got) != 1 || got[0].Data != "ok" {
		t.Fatalf("events: %+v", got)
	}
}

func TestSSE_LineOverLimitFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunk := strings.Repeat("x", 4096)
		for range 8 { // ни одного терминатора строки
			fmt.Fprint(w, chunk)
			fl.Flush()
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{Timeout: 2 * time.Second, SSEMaxEventBytes: 8 << 10})
	stream := openStream(t, c, srv.URL)
	if got := collect(stream); len(got) != 0 {
		t.Fatalf("no event can be dispatched: %+v", got)
	}
	if !errors.Is(stream.Err(), ErrEventTooLarge) {
		t.Fatalf("want ErrEventTooLarge, got %v", stream.Err())
	}
}

func TestSSE_EventOverLimitFails(t *testing.T) {
	line := "data: " + strings.Repeat("y", 1024) + "\n"
	srv := sseServer(t, strings.Repeat(line, 16)+"\n")

	c := newTestClient(t, Config{Timeout: 2 * time.Second, SSEMaxEventBytes: 4 << 10})
	stream := openStream(t, c, srv.URL)
	if got := collect(stream); len(got) != 0 {
		t.Fatalf("oversized event must not be dispatched: %+v", got)
	}
	if !errors.Is(stream.Err(), ErrEventTooLarge) {
		t.Fatalf("want ErrEventTooLarge, got %v", stream.Err())
	}
}

func TestSSE_HeaderPhaseTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{Timeout: 100 * time.Millisecond})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	start := time.Now()
	_, err := c.Stream(req)
	if !errors.Is(err, ErrHeaderTimeout) {
		t.Fatalf("want ErrHeaderTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("header timeout took %v, want ~100ms", elapsed)
	}
}

func TestSSE_BodyOutlivesTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: first\n\n")
		fl.Flush()
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: late\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	// Timeout ограничивает только заголовки: событие через 300ms > Timeout доходит.
	c := newTestClient(t, Config{Timeout: 100 * time.Millisecond})
	stream := openStream(t, c, srv.URL)
	got := collect(stream)
	if len(got) != 2 || got[1].Data != "late" {
		t.Fatalf("stream body must outlive Timeout, got %+v (err %v)", got, stream.Err())
	}
}

func TestSSE_CloseStopsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := range 1000 {
			if r.Context().Err() != nil {
				return // клиент закрыл поток (Close)
			}
			fmt.Fprintf(w, "data: %d\n\n", i)
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	stream := openStream(t, sseClient(t), srv.URL)
	<-stream.Events() // одно событие
	_ = stream.Close()
	collect(stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("Close should be clean, got %v", err)
	}
}

func countStreamReaders(t *testing.T) int {
	t.Helper()
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return bytes.Count(buf[:n], []byte("httpclient.(*SSEStream).read"))
}

// Регрессия (NF5): потребитель прервал генерацию (barge-in) — отменил ctx и
// перестал читать Events(), не вызвав Close(). Фоновая горутина обязана уйти,
// освободив вместе с собой тело ответа и соединение.
func TestSSE_ContextCancelReleasesReaderGoroutine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := range 100 { // заведомо больше буфера канала событий
			fmt.Fprintf(w, "data: %d\n\n", i)
			fl.Flush()
		}
	}))
	defer srv.Close()

	before := countStreamReaders(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	stream, err := sseClient(t).Stream(req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := <-stream.Events(); !ok {
		t.Fatal("stream ended before the first event")
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for countStreamReaders(t) > before {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("reader goroutine leaked after ctx cancel:\n%s", buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(5 * time.Millisecond)
	}

	for range stream.Events() { // канал закрыт — цикл обязан завершиться
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("ctx cancel is a clean end of stream, got %v", err)
	}
}

// Регрессия (NF6): `retry:` больше 292 лет не должен заворачиваться в
// отрицательную длительность — реконнект «немедленно» вместо «никогда».
func TestSSE_AbsurdRetryFieldSaturates(t *testing.T) {
	srv := sseServer(t, "retry: 18446744073709551615\ndata: hi\n\n")
	stream := openStream(t, sseClient(t), srv.URL)
	if got := collect(stream); len(got) != 1 {
		t.Fatalf("events: %+v", got)
	}
	if d := stream.Retry(); d != time.Duration(math.MaxInt64) {
		t.Fatalf("retry = %v, want saturated max", d)
	}
	if got := millisToDuration(1500); got != 1500*time.Millisecond {
		t.Fatalf("millisToDuration(1500) = %v", got)
	}
}

// Регрессия (NF7): Timer.Stop не ждёт уже начавшуюся функцию таймера, поэтому
// «заголовки успели» и «таймаут» гоняются. Исходов ровно два — ErrHeaderTimeout
// или живой поток; третьего (открытый поток, умерший с context canceled) быть
// не должно.
func TestSSE_HeaderTimeoutRaceHasNoThirdOutcome(t *testing.T) {
	const timeout = 20 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(timeout)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: hi\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := newTestClient(t, Config{Timeout: timeout})
	for range 20 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		stream, err := c.Stream(req)
		if err != nil {
			if !errors.Is(err, ErrHeaderTimeout) {
				t.Fatalf("dial error must be a header timeout: %v", err)
			}
			continue
		}
		got := collect(stream)
		if err := stream.Err(); err != nil {
			t.Fatalf("an opened stream must not die of the header timer: %v", err)
		}
		if len(got) != 1 || got[0].Data != "hi" {
			t.Fatalf("opened stream lost its event: %+v", got)
		}
		_ = stream.Close()
	}
}

// endGate держит OnEnd первого спана стрима до release: тест видит, что сделано до конца
// спана; следующие спаны проходят сразу.
type endGate struct {
	once           sync.Once
	ended, release chan struct{}
}

func (g *endGate) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (g *endGate) OnEnd(s sdktrace.ReadOnlySpan) {
	if s.Name() != "httpclient.Stream GET" {
		return
	}
	g.once.Do(func() {
		close(g.ended)
		<-g.release
	})
}

func (*endGate) Shutdown(context.Context) error   { return nil }
func (*endGate) ForceFlush(context.Context) error { return nil }

// Закрытый Events() — конец стрима, уже записанный в метрику и спан: потребитель, который
// по нему снимает метрики, видит status="200/sse".
func TestSSE_RecordedBeforeEventsClose(t *testing.T) {
	gate := &endGate{ended: make(chan struct{}), release: make(chan struct{})}
	defer close(gate.release)
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(gate)))
	// Прежний провайдер — глобальный делегат, вернуть его нельзя: гейт ушёл бы в чужие тесты.
	t.Cleanup(func() { otel.SetTracerProvider(sdktrace.NewTracerProvider()) })

	stream := openStream(t, sseClient(t), sseServer(t, "data: x\n\n").URL)
	if _, ok := <-stream.Events(); !ok {
		t.Fatal("no event")
	}
	<-gate.ended
	select {
	case _, ok := <-stream.Events():
		if !ok {
			t.Fatal("Events() closed before the stream span and metrics were recorded")
		}
	default:
	}
}
