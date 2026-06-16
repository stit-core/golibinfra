package sberaikit

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// StreamEvent — нормализованное событие SSE-стрима GigaChat. Поля заполнены по
// смыслу дельты: Content — инкремент текста; FunctionCall — дельта вызова функции;
// FinishReason — на финальном content-чанке.
type StreamEvent struct {
	Content      string
	FunctionCall *FunctionCall
	FinishReason FinishReason
}

// Stream — pull-итератор: Next до false, затем Final для итогового ответа
// (агрегированный текст + Usage + Cost). Под gRPC server-stream ре-стримятся
// события из Event.
type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Final() (*ChatResponse, error)
	Close() error
}

// errStream — стрим, который сразу в ошибке (сбой до открытия SSE).
type errStream struct{ err error }

func (e *errStream) Next() bool                    { return false }
func (e *errStream) Event() StreamEvent            { return StreamEvent{} }
func (e *errStream) Err() error                    { return e.err }
func (e *errStream) Final() (*ChatResponse, error) { return nil, e.err }
func (e *errStream) Close() error                  { return nil }

// chatChunk — wire-форма SSE-чанка. Usage присутствует на финальном usage-чанке
// перед [DONE] (research §5).
type chatChunk struct {
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []chunkChoice   `json:"choices"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int          `json:"index"`
	Delta        deltaMessage `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

type deltaMessage struct {
	Role         string        `json:"role,omitempty"`
	Content      string        `json:"content,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
}

// sseStream дрейнит text/event-stream, аккумулируя текст и финальный usage.
type sseStream struct {
	body    io.ReadCloser
	sc      *bufio.Scanner
	client  *Client
	ctx     context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	model   string
	start   time.Time
	content strings.Builder
	usage   Usage
	model2  string // model из чанков (точное имя от сервера)
	finish  FinishReason
	cur     StreamEvent
	err     error
	done    bool
}

// ChatStream открывает стриминговый chat/completions. Ошибка до открытия SSE —
// через Stream.Err (errStream); сам дрейн — лениво в Next.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest) Stream {
	req.Stream = true
	model := c.model(req.Model)
	req.Model = model
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	b, err := json.Marshal(req)
	if err != nil {
		return &errStream{err: errInvalid("marshal chat stream", err)}
	}
	resp, err := c.doRequest(ctx, http.MethodPost, url, b, "application/json",
		http.Header{"Accept": []string{"text/event-stream"}})
	if err != nil {
		return &errStream{err: err}
	}
	if resp.StatusCode >= 400 {
		e := classify(parseAPIError(resp.StatusCode, resp.Body))
		resp.Body.Close()
		return &errStream{err: e}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &sseStream{
		body:   resp.Body,
		sc:     sc,
		client: c,
		ctx:    ctx,
		model:  model,
		start:  c.clock.Now(),
	}
}

// Next читает следующий значимый SSE-чанк. Терминатор data: [DONE] завершает
// стрим (не JSON). Usage-чанк (без delta.content) аккумулируется, но событием
// наружу не отдаётся.
func (st *sseStream) Next() bool {
	if st.err != nil || st.done {
		return false
	}
	for st.sc.Scan() {
		line := strings.TrimSpace(st.sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			st.finish2()
			return false
		}

		var ch chatChunk
		if e := json.Unmarshal([]byte(payload), &ch); e != nil {
			st.err = errServer("decode stream chunk", e)
			st.finish2()
			return false
		}
		if ch.Model != "" {
			st.model2 = ch.Model
		}
		if len(ch.Usage) > 0 {
			st.usage = decodeUsage(ch.Usage)
		}
		if len(ch.Choices) == 0 {
			continue // usage-only чанк
		}

		c0 := ch.Choices[0]
		ev := StreamEvent{Content: c0.Delta.Content, FunctionCall: c0.Delta.FunctionCall}
		if c0.FinishReason != nil {
			ev.FinishReason = FinishReason(*c0.FinishReason)
			st.finish = ev.FinishReason
		}
		st.content.WriteString(c0.Delta.Content)

		// Пропускаем пустые служебные чанки (только role на первом).
		if ev.Content == "" && ev.FunctionCall == nil && ev.FinishReason == "" {
			continue
		}
		st.cur = ev
		return true
	}
	if e := st.sc.Err(); e != nil {
		st.err = errServer("scan stream", e)
	}
	st.finish2()
	return false
}

func (st *sseStream) Event() StreamEvent { return st.cur }

func (st *sseStream) Err() error { return st.err }

func (st *sseStream) Final() (*ChatResponse, error) {
	if st.err != nil {
		return nil, st.err
	}
	model := st.model2
	if model == "" {
		model = st.model
	}
	resp := &ChatResponse{
		Object: "chat.completion",
		Model:  model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: RoleAssistant, Content: st.content.String()},
			FinishReason: st.finish,
		}},
		Usage: st.usage,
	}
	resp.Cost = st.client.costOf(model, st.usage)
	return resp, nil
}

func (st *sseStream) Close() error { return st.body.Close() }

// finish2 — однократная запись метрик по завершении дрейна (успех/ошибка).
func (st *sseStream) finish2() {
	if st.done {
		return
	}
	st.done = true
	model := st.model2
	if model == "" {
		model = st.model
	}
	if st.err != nil {
		st.client.instr.record(st.ctx, "chat", model, "error", st.client.clock.Now().Sub(st.start), Usage{}, 0)
		return
	}
	st.client.instr.record(st.ctx, "chat", model, "ok", st.client.clock.Now().Sub(st.start), st.usage, st.client.costOf(model, st.usage))
}
