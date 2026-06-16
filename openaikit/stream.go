package openaikit

import (
	"context"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type EventType int

const (
	EventTextDelta     EventType = iota // приращение output-текста
	EventRefusalDelta                   // приращение refusal-текста
	EventToolArgsDelta                  // приращение JSON-аргументов function-call
	EventDone                           // терминатор (response.completed / [DONE])
	EventOther
)

// StreamEvent — нормализованное событие стрима. Поля заполнены по Type.
type StreamEvent struct {
	Type        EventType
	Text        string // TextDelta
	Refusal     string // RefusalDelta
	PartialJSON string // ToolArgsDelta
	ItemID      string // ключ корреляции для multi-tool-call (Responses item_id)
}

// Stream — pull-итератор: Next до false, затем Final для итогового ответа
// (текст + нормализованный Usage + Cost). Финальный авторитетный Usage берётся из
// терминирующего события: Responses — из response.completed; Chat — из последнего
// chunk с usage (требует stream_options.include_usage).
type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Final() (*ChatResponse, error)
	Close() error
}

// responsesStream — стрим Responses API. Терминатор — response.completed (НЕТ
// [DONE]); финальный response (с usage) приходит в этом событии.
type responsesStream struct {
	s      *ssestream.Stream[responses.ResponseStreamEventUnion]
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	model  string
	start  time.Time
	text   strings.Builder
	final  responses.Response
	cur    StreamEvent
	done   bool
}

func (st *responsesStream) Next() bool {
	if !st.s.Next() {
		st.finish()
		return false
	}
	ev := st.s.Current()
	switch ev.Type {
	case "response.output_text.delta":
		st.text.WriteString(ev.Delta)
		st.cur = StreamEvent{Type: EventTextDelta, Text: ev.Delta, ItemID: ev.ItemID}
	case "response.refusal.delta":
		st.cur = StreamEvent{Type: EventRefusalDelta, Refusal: ev.Delta, ItemID: ev.ItemID}
	case "response.function_call_arguments.delta":
		st.cur = StreamEvent{Type: EventToolArgsDelta, PartialJSON: ev.Delta, ItemID: ev.ItemID}
	case "response.completed", "response.failed", "response.incomplete":
		st.final = ev.Response
		st.cur = StreamEvent{Type: EventDone}
	default:
		st.cur = StreamEvent{Type: EventOther}
	}
	return true
}

func (st *responsesStream) Event() StreamEvent { return st.cur }

func (st *responsesStream) Err() error { return classify(st.s.Err()) }

func (st *responsesStream) Final() (*ChatResponse, error) {
	if err := st.Err(); err != nil {
		return nil, err
	}
	u := mapResponsesUsage(st.final.Usage)
	model := st.final.Model
	if model == "" {
		model = st.model
	}
	return &ChatResponse{
		ID:    st.final.ID,
		Model: model,
		Text:  st.text.String(),
		Usage: u,
		Cost:  st.client.costOf(model, u),
	}, nil
}

func (st *responsesStream) Close() error { return st.s.Close() }

func (st *responsesStream) finish() {
	if st.done {
		return
	}
	st.done = true
	if err := st.Err(); err != nil {
		st.client.instr.record(st.ctx, st.model, ModalityText, "error", time.Since(st.start), Usage{}, 0)
		return
	}
	u := mapResponsesUsage(st.final.Usage)
	st.client.instr.record(st.ctx, st.model, ModalityText, "ok", time.Since(st.start), u, st.client.costOf(st.model, u))
}

// chatStream — стрим Chat Completions. Терминатор — [DONE] (SDK закрывает Next);
// usage приходит в финальном chunk с пустым choices (нужен include_usage).
type chatStream struct {
	s      *ssestream.Stream[openai.ChatCompletionChunk]
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	model  string
	start  time.Time
	text   strings.Builder
	id     string
	usage  openai.CompletionUsage
	cur    StreamEvent
	done   bool
}

func (st *chatStream) Next() bool {
	if !st.s.Next() {
		st.finish()
		return false
	}
	chunk := st.s.Current()
	if chunk.ID != "" {
		st.id = chunk.ID
	}
	// Финальный chunk несёт usage с пустым choices.
	if chunk.Usage.TotalTokens > 0 || len(chunk.Choices) == 0 {
		st.usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		st.cur = StreamEvent{Type: EventOther}
		return true
	}
	d := chunk.Choices[0].Delta
	switch {
	case d.Content != "":
		st.text.WriteString(d.Content)
		st.cur = StreamEvent{Type: EventTextDelta, Text: d.Content}
	case d.Refusal != "":
		st.cur = StreamEvent{Type: EventRefusalDelta, Refusal: d.Refusal}
	default:
		st.cur = StreamEvent{Type: EventOther}
	}
	return true
}

func (st *chatStream) Event() StreamEvent { return st.cur }

func (st *chatStream) Err() error { return classify(st.s.Err()) }

func (st *chatStream) Final() (*ChatResponse, error) {
	if err := st.Err(); err != nil {
		return nil, err
	}
	u := mapChatUsage(st.usage)
	return &ChatResponse{
		ID:    st.id,
		Model: st.model,
		Text:  st.text.String(),
		Usage: u,
		Cost:  st.client.costOf(st.model, u),
	}, nil
}

func (st *chatStream) Close() error { return st.s.Close() }

func (st *chatStream) finish() {
	if st.done {
		return
	}
	st.done = true
	if err := st.Err(); err != nil {
		st.client.instr.record(st.ctx, st.model, ModalityText, "error", time.Since(st.start), Usage{}, 0)
		return
	}
	u := mapChatUsage(st.usage)
	st.client.instr.record(st.ctx, st.model, ModalityText, "ok", time.Since(st.start), u, st.client.costOf(st.model, u))
}
