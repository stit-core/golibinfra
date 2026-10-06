package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"git.stit.tech/stit-core/golibinfra/internal/requestid"
)

var (
	// ErrNotEventStream — ответ на Stream пришёл не с Content-Type text/event-stream.
	ErrNotEventStream = errors.New("httpclient: not an event stream")
	// ErrEventTooLarge — строка или событие SSE превысили Config.SSEMaxEventBytes.
	ErrEventTooLarge = errors.New("httpclient: sse event too large")
	// ErrHeaderTimeout — заголовки ответа на Stream не пришли за Config.Timeout.
	ErrHeaderTimeout = errors.New("httpclient: response headers timeout")
)

// StatusError — неуспешный статус там, где ответ нельзя вернуть вызывающему
// как *http.Response (Stream). Достаётся через errors.As.
type StatusError struct {
	Status int
	Body   string // тело ответа, усечённое до 2 KiB
}

// Error — «status <код>: <тело>», без тела — «status <код>».
func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("status %d", e.Status)
	}
	return fmt.Sprintf("status %d: %s", e.Status, e.Body)
}

// Event — одно SSE-событие. Поля по спеке WHATWG (data склеены через "\n");
// ID — last-event-id, он живёт между событиями потока.
type Event struct {
	ID    string
	Event string // тип события из поля "event:" ("" = безымянное, дефолт "message")
	Data  string
}

// SSEStream — активный SSE-поток. Events() закрывается при EOF/ошибке/отмене
// ctx/Close — после записи метрики и конца спана стрима; после закрытия Err() даёт
// терминальную причину (nil = чистый EOF или отмена ctx).
type SSEStream struct {
	events chan Event
	done   chan struct{}
	body   io.ReadCloser
	ctx    context.Context // ctx вызывающего: его отмена — штатное завершение
	cancel context.CancelFunc
	limit  int

	closeOnce sync.Once
	mu        sync.Mutex
	err       error
	lastID    string
	retry     time.Duration
}

// Stream выполняет req как SSE-поток без retry и breaker'а: частично прочитанный
// стрим неретраябелен. Config.Timeout ограничивает только ожидание заголовков —
// тело живёт, пока жив ctx вызывающего или пока не вызван Close.
func (c *Client) Stream(req *http.Request) (*SSEStream, error) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	if id := requestid.From(req.Context()); id != "" && req.Header.Get(requestid.Header) == "" {
		req.Header.Set(requestid.Header, id)
	}

	callerCtx, span := c.startSpan(req, "httpclient.Stream")
	streamCtx, cancel := context.WithCancel(callerCtx)
	start := time.Now()

	// Timer.Stop не ждёт уже начавшуюся функцию таймера: гонку «заголовки
	// пришли» / «таймер сработал» решает CAS, иначе таймер отменяет streamCtx
	// уже открытого потока и тот падает необъяснимым context canceled.
	var headersSettled atomic.Bool
	var deadline *time.Timer
	if c.cfg.Timeout > 0 {
		deadline = time.AfterFunc(c.cfg.Timeout, func() {
			if headersSettled.CompareAndSwap(false, true) {
				cancel()
			}
		})
	}
	resp, err := c.streamC.Do(req.WithContext(streamCtx)) //nolint:bodyclose // тело живёт до SSEStream.Close; на ошибочных ветках — drain(resp)
	if deadline != nil {
		deadline.Stop()
	}
	headersTimedOut := !headersSettled.CompareAndSwap(false, true)
	if err != nil {
		cancel()
		if headersTimedOut && callerCtx.Err() == nil {
			err = fmt.Errorf("%w: %w", ErrHeaderTimeout, err)
		}
		err = fmt.Errorf("httpclient: sse dial: %w", err)
		c.endStream(callerCtx, span, req, start, nil, err)
		return nil, err
	}
	if headersTimedOut {
		// Таймер выиграл гонку и уже отменил streamCtx: тело нежизнеспособно.
		drain(resp)
		cancel()
		err = fmt.Errorf("httpclient: sse dial: %w", ErrHeaderTimeout)
		c.endStream(callerCtx, span, req, start, nil, err)
		return nil, err
	}
	if err := streamResponseErr(resp); err != nil {
		drain(resp)
		cancel()
		c.endStream(callerCtx, span, req, start, resp, err)
		return nil, err
	}

	s := &SSEStream{
		events: make(chan Event, 16),
		done:   make(chan struct{}),
		body:   resp.Body,
		ctx:    callerCtx,
		cancel: cancel,
		limit:  c.cfg.SSEMaxEventBytes,
	}
	go s.read(func() {
		// Оборвавшийся посреди тела стрим — status="error": заголовки 200 уже
		// ничего не говорят о результате.
		if err := s.Err(); err != nil {
			c.endStream(callerCtx, span, req, start, nil, err)
			return
		}
		c.endStream(callerCtx, span, req, start, resp, nil)
	})
	return s, nil
}

func streamResponseErr(resp *http.Response) error {
	if resp.StatusCode >= http.StatusBadRequest {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("httpclient: sse: %w", &StatusError{
			Status: resp.StatusCode,
			Body:   strings.TrimSpace(string(snippet)),
		})
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "text/event-stream" {
		return fmt.Errorf("%w: %q", ErrNotEventStream, resp.Header.Get("Content-Type"))
	}
	return nil
}

// Events — канал событий потока. Закрывается при завершении (см. SSEStream).
func (s *SSEStream) Events() <-chan Event { return s.events }

// Err — терминальная ошибка ПОСЛЕ закрытия Events(). nil при чистом EOF или отмене ctx.
func (s *SSEStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// LastEventID — последний виденный `id:` потока (спека: Last-Event-ID для
// реконнекта, который httpclient не делает сам).
func (s *SSEStream) LastEventID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastID
}

// Retry — интервал реконнекта из поля `retry:`, 0 если сервер его не слал.
func (s *SSEStream) Retry() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retry
}

// Close принудительно завершает поток (закрывает тело, будит блокирующий Read и
// останавливает горутину). Идемпотентен; необязателен, если поток завершается
// отменой ctx.
func (s *SSEStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.body.Close()
		s.cancel()
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

func (s *SSEStream) setStreamState(lastID string, retry time.Duration) {
	s.mu.Lock()
	s.lastID, s.retry = lastID, retry
	s.mu.Unlock()
}

func (s *SSEStream) read(record func()) {
	// Метрика и спан — до закрытия Events(): конец стрима потребитель видит уже записанным.
	defer close(s.events)
	defer record()
	defer s.cancel()
	defer s.body.Close()

	sc := bufio.NewScanner(s.body)
	sc.Buffer(make([]byte, 0, min(initialLineBuffer, s.limit)), s.limit)
	sc.Split(scanEventLines)

	p := sseParser{limit: s.limit}
	var pubID string
	var pubRetry time.Duration
	for sc.Scan() {
		ev, dispatch, err := p.feed(sc.Bytes())
		if err != nil {
			s.setErr(err)
			return
		}
		if p.lastID != pubID || p.retry != pubRetry {
			pubID, pubRetry = p.lastID, p.retry
			s.setStreamState(pubID, pubRetry)
		}
		if dispatch && !s.emit(ev) {
			return // Close() или отмена потребителя
		}
	}
	// Отмена ctx и Close() — норма (Err остаётся nil); незавершённое последнее
	// событие без финальной пустой строки дропаем (спека).
	if err := sc.Err(); err != nil && s.ctx.Err() == nil && !s.closed() {
		if errors.Is(err, bufio.ErrTooLong) {
			err = fmt.Errorf("%w: line over %d bytes", ErrEventTooLarge, s.limit)
		}
		s.setErr(err)
	}
}

// emit отправляет событие, уважая Close() и отмену ctx вызывающего.
// false = поток нужно остановить. Ветка ctx обязательна: потребитель, ушедший
// по отмене (barge-in) и переставший читать, иначе оставил бы горутину и
// соединение висеть на переполненном канале навсегда.
func (s *SSEStream) emit(ev Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.done:
		return false
	case <-s.ctx.Done():
		return false
	}
}

const initialLineBuffer = 64 << 10

// scanEventLines режет поток на строки по CR, LF или CRLF: спека допускает
// любой из трёх терминаторов.
func scanEventLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		if i+1 < len(data) {
			if data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		if atEOF {
			return i + 1, data[:i], nil
		}
		return 0, nil, nil // ждём байт после CR, чтобы отличить CR от CRLF
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// sseParser — автомат полей SSE (WHATWG): data склеиваются через "\n", тип
// события сбрасывается вместе с data-буфером, last-event-id живёт между
// событиями.
type sseParser struct {
	limit     int
	data      []byte
	hasData   bool
	eventType string
	lastID    string
	retry     time.Duration
}

// feed скармливает одну строку без терминатора; dispatch=true — блок кончился и
// событие пора отдавать потребителю.
func (p *sseParser) feed(line []byte) (ev Event, dispatch bool, err error) {
	if len(line) == 0 {
		ev, dispatch = p.dispatch()
		return ev, dispatch, nil
	}
	if line[0] == ':' {
		return Event{}, false, nil // комментарий/heartbeat
	}
	field, value, found := bytes.Cut(line, []byte{':'})
	if found {
		value = bytes.TrimPrefix(value, []byte{' '}) // спека: один ведущий пробел отбрасывается
	}
	switch string(field) {
	case "data":
		if len(p.data)+len(value)+1 > p.limit {
			return Event{}, false, fmt.Errorf("%w: over %d bytes", ErrEventTooLarge, p.limit)
		}
		if p.hasData {
			p.data = append(p.data, '\n')
		}
		p.data = append(p.data, value...)
		p.hasData = true
	case "event":
		p.eventType = string(value)
	case "id":
		if !bytes.ContainsRune(value, 0) { // спека: id с NUL игнорируется
			p.lastID = string(value)
		}
	case "retry":
		if ms, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			p.retry = millisToDuration(ms)
		}
	}
	return Event{}, false, nil
}

// millisToDuration насыщает максимумом вместо переполнения: сервер вправе
// прислать `retry:` больше 292 лет, а завёрнутое отрицательное значение
// прочиталось бы как «реконнект немедленно».
func millisToDuration(ms uint64) time.Duration {
	if ms > uint64(math.MaxInt64)/uint64(time.Millisecond) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
}

func (p *sseParser) dispatch() (Event, bool) {
	if !p.hasData {
		p.eventType = "" // пустой data-буфер сбрасывает и тип события (спека)
		return Event{}, false
	}
	ev := Event{ID: p.lastID, Event: p.eventType, Data: string(p.data)}
	p.data, p.hasData, p.eventType = p.data[:0], false, ""
	return ev, true
}

func (c *Client) endStream(ctx context.Context, span trace.Span, req *http.Request, start time.Time, resp *http.Response, err error) {
	host, _ := c.hosts.label(req.URL.Host)
	c.instr.requestDuration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(
			attribute.String("host", host),
			attribute.String("method", req.Method),
			attribute.String("status", statusLabel(resp, err)),
			attribute.String("mode", "sse"),
		),
	)
	recordSpanResult(span, resp, err)
	span.End()
}
