package yandexaikit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// StreamEvent — нормализованное событие native-стрима. Native-стрим **cumulative**:
// каждый чанк несёт ПОЛНЫЙ текст так-далеко (не дельту) — Text перезаписывают, не
// аккумулируют. Терминальный чанк — Status.IsFinal(); usage обычно на нём.
type StreamEvent struct {
	Text   string            // полный текст так-далеко (cumulative)
	Status AlternativeStatus // PARTIAL → FINAL/TRUNCATED_FINAL/CONTENT_FILTER/TOOL_CALLS
	Usage  Usage             // обычно заполнен на финальном чанке
}

// Stream — pull-итератор: Next до false, затем Final для итогового Response
// (последний чанк + Usage + Cost). Под gRPC server-stream ре-стримятся события.
type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Final() (*Response, error)
	Close() error
}

// errStream — стрим сразу в ошибке (сбой до открытия тела).
type errStream struct{ err error }

func (e *errStream) Next() bool                { return false }
func (e *errStream) Event() StreamEvent        { return StreamEvent{} }
func (e *errStream) Err() error                { return e.err }
func (e *errStream) Final() (*Response, error) { return nil, e.err }
func (e *errStream) Close() error              { return nil }

// jsonStream — итератор над телом native-стрима (поток JSON-объектов
// {result:…}, доставляемых chunked). json.Decoder читает объекты подряд.
type jsonStream struct {
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	body   io.ReadCloser
	dec    *json.Decoder
	model  string
	start  time.Time

	cur  StreamEvent
	last CompletionResult
	err  error
	done bool
}

// Stream открывает стриминговый completion. Ошибка маппинга/транспорта до тела —
// через Stream.Err (errStream).
func (c *Client) Stream(ctx context.Context, req Request) Stream {
	if err := req.validateResponseFormat(); err != nil {
		return &errStream{err: err}
	}
	body, model := c.toCompletionRequest(req, true)

	b, err := encodeJSON(body)
	if err != nil {
		return &errStream{err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+completionPath, b)
	if err != nil {
		return &errStream{err: err}
	}
	c.setHeaders(httpReq, true)
	if err := c.auth.apply(ctx, httpReq); err != nil {
		return &errStream{err: err}
	}

	start := c.clock.Now()
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		c.recordCall(ctx, "completion-stream", model, "error", c.clock.Now().Sub(start))
		return &errStream{err: err}
	}
	if resp.StatusCode >= 400 {
		c.recordCall(ctx, "completion-stream", model, strconv.Itoa(resp.StatusCode), c.clock.Now().Sub(start))
		e := classify(resp.StatusCode, parseAPIError(resp))
		resp.Body.Close()
		return &errStream{err: e}
	}

	return &jsonStream{
		client: c,
		ctx:    ctx,
		body:   resp.Body,
		dec:    json.NewDecoder(resp.Body),
		model:  model,
		start:  start,
	}
}

func (s *jsonStream) Next() bool {
	if s.err != nil || s.done {
		return false
	}
	var env CompletionEnvelope
	if err := s.dec.Decode(&env); err != nil {
		if err != io.EOF { //nolint:errorlint // io.EOF — sentinel, сравнение прямое
			s.err = err
		}
		s.finish()
		return false
	}
	s.last = env.Result
	ev := StreamEvent{Usage: usageFromContent(env.Result.Usage)}
	if len(env.Result.Alternatives) > 0 {
		a := env.Result.Alternatives[0]
		ev.Text = a.Message.Text
		ev.Status = a.Status
	}
	s.cur = ev
	return true
}

func (s *jsonStream) Event() StreamEvent { return s.cur }

func (s *jsonStream) Err() error { return s.err }

func (s *jsonStream) Final() (*Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.client.buildResponse(s.last, s.model, false), nil
}

func (s *jsonStream) Close() error { return s.body.Close() }

// finish — однократная запись метрик по завершении дрейна (успех/ошибка).
func (s *jsonStream) finish() {
	if s.done {
		return
	}
	s.done = true
	status := "ok"
	if s.err != nil {
		status = "error"
	}
	s.client.recordCall(s.ctx, "completion-stream", s.model, status, s.client.clock.Now().Sub(s.start))
	if s.err == nil {
		u := usageFromContent(s.last.Usage)
		s.client.instr.recordUsage(s.ctx, s.model, u, s.client.costOf(s.model, u))
	}
}
