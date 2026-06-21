package httpclient

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"git.stit.tech/stit-core/golibinfra/httpkit"
)

// sse.go — поддержка Server-Sent Events (text/event-stream) для long-lived стримов
// (LLM token-stream vLLM/OpenAI и т.п.). Принципиально отличается от Do:
//   - НЕТ общего Timeout (стрим живёт пока жив req.Context — отмена ctx рвёт стрим);
//   - НЕТ retry/circuit-breaker (частично прочитанный стрим неретраябелен; breaker
//     рассчитан на короткие запросы, его Timeout open-state не применим к стриму).
// Отмена: создавайте req с context.WithCancel и cancel() = barge-in/hangup, либо
// вызывайте SSEStream.Close(). И то, и другое завершает фоновую горутину без утечки.

// Event — одно SSE-событие. Поля по спеке WHATWG (data склеены через "\n").
// Терминатор потока (напр. OpenAI "[DONE]") приходит как обычный Event{Data:"[DONE]"} —
// httpclient не интерпретирует полезную нагрузку, это делает вызывающий.
type Event struct {
	ID    string
	Event string // тип события из поля "event:" ("" = безымянное, дефолт "message")
	Data  string
}

// SSEStream — активный SSE-поток. Events() закрывается при EOF/ошибке/отмене ctx/Close.
// После закрытия Events() читайте Err() для терминальной причины (nil = чистый EOF или
// отмена ctx — норма для barge-in).
type SSEStream struct {
	events chan Event
	done   chan struct{}
	body   io.ReadCloser
	ctx    context.Context

	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

// Stream выполняет req как SSE-поток. Возвращает ошибку синхронно, если соединение не
// удалось или сервер ответил не-2xx (тело прочитано в текст ошибки). При успехе фоновая
// горутина парсит события в Events(). Заголовок Accept выставляется в text/event-stream,
// если вызывающий не задал свой; X-Request-ID прокидывается из ctx как в Do.
func (c *Client) Stream(req *http.Request) (*SSEStream, error) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	if id := httpkit.RequestIDFrom(req.Context()); id != "" {
		req.Header.Set(httpkit.HeaderRequestID, id)
	}

	start := time.Now()
	resp, err := c.streamC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpclient: sse dial: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		c.recordStream(req, start, resp.StatusCode)
		return nil, fmt.Errorf("httpclient: sse status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	s := &SSEStream{
		events: make(chan Event, 16),
		done:   make(chan struct{}),
		body:   resp.Body,
		ctx:    req.Context(),
	}
	go s.read(func() { c.recordStream(req, start, resp.StatusCode) })
	return s, nil
}

// Events — канал событий потока. Закрывается при завершении (см. SSEStream).
func (s *SSEStream) Events() <-chan Event { return s.events }

// Err — терминальная ошибка ПОСЛЕ закрытия Events(). nil при чистом EOF или отмене ctx.
func (s *SSEStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close принудительно завершает поток (закрывает тело, будит блокирующий Read и останавливает
// горутину). Идемпотентен. Вызывать необязательно, если поток завершается отменой ctx.
func (s *SSEStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.body.Close()
	})
	return nil
}

func (s *SSEStream) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *SSEStream) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *SSEStream) read(record func()) {
	defer record()
	defer close(s.events)
	defer s.body.Close()

	r := bufio.NewReader(s.body)
	var ev Event
	hasData := false

	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			if dispatch := s.feed(&ev, &hasData, line); dispatch {
				if !s.emit(ev) {
					return // Close() или отмена потребителя
				}
				ev, hasData = Event{}, false
			}
		}
		if err != nil {
			// EOF = чистое завершение; отмена ctx или Close() = норма (Err остаётся nil).
			if err != io.EOF && s.ctx.Err() == nil && !s.closed() {
				s.setErr(err)
			}
			// Незавершённое последнее событие без финального blank-line дропаем (спека).
			return
		}
	}
}

// feed разбирает одну строку SSE. Возвращает true, если строка — пустой разделитель,
// т.е. накопленное событие пора диспатчить.
func (s *SSEStream) feed(ev *Event, hasData *bool, raw string) bool {
	line := strings.TrimRight(raw, "\r\n")
	if line == "" {
		return *hasData // blank line = конец события (диспатч только если были data)
	}
	if strings.HasPrefix(line, ":") {
		return false // комментарий/heartbeat
	}
	field, value, found := strings.Cut(line, ":")
	if found && strings.HasPrefix(value, " ") {
		value = value[1:] // спека: один ведущий пробел после двоеточия отбрасывается
	}
	if !found {
		field, value = line, "" // поле без двоеточия = имя поля, пустое значение
	}
	switch field {
	case "data":
		if *hasData {
			ev.Data += "\n"
		}
		ev.Data += value
		*hasData = true
	case "event":
		ev.Event = value
	case "id":
		ev.ID = value
	case "retry":
		// reconnection-time: на стороне клиента не реализуем (стрим неретраябелен) — игнор.
	}
	return false
}

// emit отправляет событие, уважая Close()/done. false = поток нужно остановить.
func (s *SSEStream) emit(ev Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.done:
		return false
	}
}

func (c *Client) recordStream(req *http.Request, start time.Time, status int) {
	c.instr.requestDuration.Record(req.Context(), time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("host", req.URL.Host),
			attribute.String("method", req.Method),
			attribute.String("status", statusLabel(status)),
			attribute.String("mode", "sse"),
		),
	)
}

func statusLabel(status int) string {
	if status == 0 {
		return "error"
	}
	return fmt.Sprintf("%d", status)
}
