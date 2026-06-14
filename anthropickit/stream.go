package anthropickit

import (
	"context"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
)

type EventType int

const (
	EventMessageStart EventType = iota
	EventTextDelta
	EventThinkingDelta
	EventToolUseStart
	EventToolUseDelta
	EventBlockStop
	EventMessageDelta
	EventDone
	EventOther
)

// StreamEvent — нормализованное событие стрима. Поля заполнены по Type.
// Usage в MessageDelta кумулятивен, но cache-write split там недоступен —
// авторитетный финал берут из Final (аккумулированный Message.Usage).
type StreamEvent struct {
	Type        EventType
	Text        string     // TextDelta
	Thinking    string     // ThinkingDelta
	ToolName    string     // ToolUseStart
	ToolUseID   string     // ToolUseStart
	PartialJSON string     // ToolUseDelta
	StopReason  StopReason // MessageDelta
	Usage       *Usage     // MessageStart (вход/кэш) / MessageDelta (кумулятивно)
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

// errStream — стрим, который сразу в ошибке (сбой маппинга запроса до открытия SSE).
type errStream struct{ err error }

func (e *errStream) Next() bool                { return false }
func (e *errStream) Event() StreamEvent        { return StreamEvent{} }
func (e *errStream) Err() error                { return e.err }
func (e *errStream) Final() (*Response, error) { return nil, e.err }
func (e *errStream) Close() error              { return nil }

type sseStream struct {
	s      *ssestream.Stream[anthropic.MessageStreamEventUnion]
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	model  string
	start  time.Time
	acc    anthropic.Message
	cur    StreamEvent
	err    error
	done   bool
}

func (st *sseStream) Next() bool {
	if st.err != nil {
		return false
	}
	if !st.s.Next() {
		st.finish()
		return false
	}
	ev := st.s.Current()
	if e := st.acc.Accumulate(ev); e != nil {
		st.err = e
		st.finish()
		return false
	}
	st.cur = translate(ev)
	return true
}

func (st *sseStream) Event() StreamEvent { return st.cur }

func (st *sseStream) Err() error {
	if st.err != nil {
		return st.err
	}
	return classify(st.s.Err())
}

func (st *sseStream) Final() (*Response, error) {
	if err := st.Err(); err != nil {
		return nil, err
	}
	return st.client.buildResponse(&st.acc, st.model), nil
}

func (st *sseStream) Close() error { return st.s.Close() }

// finish — однократная запись метрик по завершении дрейна (успех/ошибка).
func (st *sseStream) finish() {
	if st.done {
		return
	}
	st.done = true
	if err := st.Err(); err != nil {
		st.client.instr.record(st.ctx, st.model, "error", time.Since(st.start), Usage{}, 0)
		return
	}
	u := mapUsage(st.acc.Usage)
	st.client.instr.record(st.ctx, st.model, "ok", time.Since(st.start), u, st.client.costOf(st.model, u))
}

func translate(ev anthropic.MessageStreamEventUnion) StreamEvent {
	switch ev.Type {
	case "message_start":
		u := mapUsage(ev.Message.Usage)
		return StreamEvent{Type: EventMessageStart, Usage: &u}
	case "content_block_start":
		if ev.ContentBlock.Type == "tool_use" {
			return StreamEvent{Type: EventToolUseStart, ToolName: ev.ContentBlock.Name, ToolUseID: ev.ContentBlock.ID}
		}
		return StreamEvent{Type: EventOther}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			return StreamEvent{Type: EventTextDelta, Text: ev.Delta.Text}
		case "thinking_delta":
			return StreamEvent{Type: EventThinkingDelta, Thinking: ev.Delta.Thinking}
		case "input_json_delta":
			return StreamEvent{Type: EventToolUseDelta, PartialJSON: ev.Delta.PartialJSON}
		}
		return StreamEvent{Type: EventOther}
	case "content_block_stop":
		return StreamEvent{Type: EventBlockStop}
	case "message_delta":
		u := mapDeltaUsage(ev.Usage)
		return StreamEvent{Type: EventMessageDelta, StopReason: StopReason(ev.Delta.StopReason), Usage: &u}
	case "message_stop":
		return StreamEvent{Type: EventDone}
	default:
		return StreamEvent{Type: EventOther}
	}
}
