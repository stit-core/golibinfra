package googleaikit

import (
	"context"
	"iter"
	"time"

	"google.golang.org/genai"
)

type EventType int

const (
	EventTextDelta EventType = iota
	EventThoughtDelta
	EventFunctionCall
	EventMediaDelta
	EventMessageDelta // несёт StopReason и/или кумулятивный Usage
	EventDone
	EventOther
)

// StreamEvent — нормализованное событие стрима. Поля заполнены по Type. genai
// шлёт дельты как партиальные GenerateContentResponse: text — дельта, usageMetadata
// кумулятивен и может опаздывать — авторитетный финал берут из Final.
type StreamEvent struct {
	Type       EventType
	Text       string // TextDelta
	Thought    string // ThoughtDelta
	ToolName   string // FunctionCall
	ToolUseID  string // FunctionCall
	ToolArgs   map[string]any
	MIMEType   string // MediaDelta
	Data       []byte // MediaDelta
	StopReason StopReason
	Usage      *Usage // MessageDelta (кумулятивно)
}

// Stream — pull-итератор: Next до false, затем Final для итогового ответа
// (Usage+Cost). Под gRPC server-stream ре-стримятся события из Event.
type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Final() (*Response, error)
	Close() error
}

// Stream открывает стриминговый generateContent (genai сам ставит alt=sse). Ошибка
// маппинга запроса — через Stream.Err (errStream), сам дрейн — лениво в Next.
func (c *Client) Stream(ctx context.Context, req Request) Stream {
	model := c.modelOr(req.Model)
	contents, cfg, err := c.toParams(req)
	if err != nil {
		return &errStream{err: err}
	}
	seq := c.sdk.Models.GenerateContentStream(ctx, model, contents, cfg)
	st := &seqStream{client: c, ctx: ctx, model: model, start: c.clock.Now()}
	st.next, st.stop = iter.Pull2(seq)
	return st
}

// errStream — стрим, который сразу в ошибке (сбой маппинга запроса до открытия SSE).
type errStream struct{ err error }

func (e *errStream) Next() bool                { return false }
func (e *errStream) Event() StreamEvent        { return StreamEvent{} }
func (e *errStream) Err() error                { return e.err }
func (e *errStream) Final() (*Response, error) { return nil, e.err }
func (e *errStream) Close() error              { return nil }

// seqStream адаптирует genai iter.Seq2 в pull-итератор Stream и аккумулирует
// контент/usage/stopReason для Final.
type seqStream struct {
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	model  string
	start  time.Time

	next func() (*genai.GenerateContentResponse, error, bool)
	stop func()

	cur          StreamEvent
	content      []Block
	usage        Usage
	finishReason StopReason
	respID       string
	modelV       string
	err          error
	done         bool
}

func (st *seqStream) Next() bool {
	if st.err != nil {
		return false
	}
	chunk, err, ok := st.next()
	if err != nil {
		st.err = classify(err)
		st.finish()
		return false
	}
	if !ok {
		st.finish()
		return false
	}
	st.accumulate(chunk)
	st.cur = translate(chunk)
	return true
}

func (st *seqStream) Event() StreamEvent { return st.cur }

func (st *seqStream) Err() error { return st.err }

func (st *seqStream) Final() (*Response, error) {
	if st.err != nil {
		return nil, st.err
	}
	model := st.modelV
	if model == "" {
		model = st.model
	}
	return &Response{
		ID:         st.respID,
		Model:      model,
		Content:    st.content,
		StopReason: st.finishReason,
		Usage:      st.usage,
		Cost:       st.client.costOf(st.model, st.usage),
	}, nil
}

func (st *seqStream) Close() error {
	st.stop()
	return nil
}

// accumulate собирает финальный ответ из дельт: текст конкатенируется, usage
// перезаписывается последним непустым (кумулятивен; может опаздывать на хвосте).
func (st *seqStream) accumulate(chunk *genai.GenerateContentResponse) {
	if chunk.ResponseID != "" {
		st.respID = chunk.ResponseID
	}
	if chunk.ModelVersion != "" {
		st.modelV = chunk.ModelVersion
	}
	if u := mapUsage(chunk.UsageMetadata); u.TotalTokens() > 0 {
		st.usage = u
	}
	if len(chunk.Candidates) == 0 {
		return
	}
	cand := chunk.Candidates[0]
	if cand.FinishReason != "" {
		st.finishReason = StopReason(cand.FinishReason)
	}
	if cand.Content == nil {
		return
	}
	for _, p := range cand.Content.Parts {
		switch {
		case p == nil:
			continue
		case p.Thought:
			st.content = append(st.content, ThoughtBlock{Text: p.Text, Signature: p.ThoughtSignature})
		case p.Text != "":
			st.appendText(p.Text)
		case p.FunctionCall != nil:
			st.content = append(st.content, FunctionCallBlock{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
		case p.InlineData != nil:
			st.content = append(st.content, MediaBlock{MIMEType: p.InlineData.MIMEType, Data: p.InlineData.Data})
		}
	}
}

// appendText мерджит текст-дельту в последний TextBlock, иначе добавляет новый.
func (st *seqStream) appendText(text string) {
	if n := len(st.content); n > 0 {
		if t, ok := st.content[n-1].(TextBlock); ok {
			st.content[n-1] = TextBlock{Text: t.Text + text}
			return
		}
	}
	st.content = append(st.content, TextBlock{Text: text})
}

// finish — однократная запись метрик по завершении дрейна (успех/ошибка).
func (st *seqStream) finish() {
	if st.done {
		return
	}
	st.done = true
	st.stop()
	if st.err != nil {
		st.client.instr.record(st.ctx, st.model, "generate", "error", st.client.clock.Now().Sub(st.start), Usage{}, 0)
		return
	}
	st.client.instr.record(st.ctx, st.model, "generate", "ok", st.client.clock.Now().Sub(st.start), st.usage, st.client.costOf(st.model, st.usage))
}

func translate(chunk *genai.GenerateContentResponse) StreamEvent {
	if u := mapUsage(chunk.UsageMetadata); u.TotalTokens() > 0 {
		ev := StreamEvent{Type: EventMessageDelta, Usage: &u}
		if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
			ev.StopReason = StopReason(chunk.Candidates[0].FinishReason)
		}
		return ev
	}
	if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
		return StreamEvent{Type: EventOther}
	}
	cand := chunk.Candidates[0]
	for _, p := range cand.Content.Parts {
		switch {
		case p == nil:
			continue
		case p.Thought:
			return StreamEvent{Type: EventThoughtDelta, Thought: p.Text}
		case p.Text != "":
			return StreamEvent{Type: EventTextDelta, Text: p.Text}
		case p.FunctionCall != nil:
			return StreamEvent{Type: EventFunctionCall, ToolName: p.FunctionCall.Name, ToolUseID: p.FunctionCall.ID, ToolArgs: p.FunctionCall.Args}
		case p.InlineData != nil:
			return StreamEvent{Type: EventMediaDelta, MIMEType: p.InlineData.MIMEType, Data: p.InlineData.Data}
		}
	}
	if cand.FinishReason != "" {
		return StreamEvent{Type: EventMessageDelta, StopReason: StopReason(cand.FinishReason)}
	}
	return StreamEvent{Type: EventOther}
}
