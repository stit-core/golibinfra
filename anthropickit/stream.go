package anthropickit

import (
	"context"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"go.opentelemetry.io/otel/trace"
)

// EventType — тип нормализованного события стрима.
type EventType int

// Типы событий стрима.
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
// авторитетный финал берут из Final.
type StreamEvent struct {
	Type        EventType
	Text        string     // TextDelta
	Thinking    string     // ThinkingDelta
	ToolName    string     // ToolUseStart
	ToolUseID   string     // ToolUseStart
	PartialJSON string     // ToolUseDelta
	StopReason  StopReason // MessageDelta
	Usage       *Usage     // MessageStart (input/cache) / MessageDelta (cumulative)
}

// Stream — pull-итератор: Next до false, затем Final для итогового ответа
// (Usage+Cost); Close обязателен — он отдаёт соединение и дописывает метрики
// брошенного стрима. Usage читается в любой момент, в том числе после обрыва.
//
// Next/Event/Err/Final вызываются из одной горутины; Close и Usage — из любой,
// в том числе параллельно с Next (Close разблокирует чтение SSE, метрики
// пишутся ровно один раз).
type Stream interface {
	Next() bool
	Event() StreamEvent
	Err() error
	Usage() Usage
	Final() (*Response, error)
	Close() error
}

// errStream — стрим, который сразу в ошибке (сбой маппинга запроса до HTTP-вызова).
type errStream struct{ err error }

func (e *errStream) Next() bool                { return false }
func (e *errStream) Event() StreamEvent        { return StreamEvent{} }
func (e *errStream) Err() error                { return e.err }
func (e *errStream) Usage() Usage              { return Usage{} }
func (e *errStream) Final() (*Response, error) { return nil, e.err }
func (e *errStream) Close() error              { return nil }

type sseStream struct {
	s      *ssestream.Stream[anthropic.MessageStreamEventUnion]
	client *Client
	ctx    context.Context //nolint:containedctx // удерживаем для финальной записи метрик
	span   trace.Span
	model  string
	start  time.Time

	mu     sync.Mutex
	acc    anthropic.Message
	cur    StreamEvent
	err    error
	ended  bool // SSE исчерпан или упал
	done   bool // метрики записаны
	closed bool
	inNext bool // горутина сейчас внутри чтения SSE
}

func (st *sseStream) Next() bool {
	st.mu.Lock()
	if st.err != nil || st.ended || st.closed {
		st.mu.Unlock()
		return false
	}
	st.inNext = true
	st.mu.Unlock()

	// Чтение SSE — без мьютекса: под ним Close ждал бы следующего события вместо
	// того, чтобы разорвать соединение.
	ok := st.s.Next()

	st.mu.Lock()
	defer st.mu.Unlock()
	st.inNext = false
	// Close уже пометил стрим брошенным и оставил учёт этой горутине: только она
	// вправе трогать состояние SDK-стрима. Ошибка чтения здесь — следствие нашего
	// же разрыва соединения, сбоем вызова она не является.
	if st.closed {
		st.finishLocked(statusAbandoned, nil)
		return false
	}
	if !ok {
		st.ended = true
		st.finishLocked(statusOK, st.errLocked())
		return false
	}
	ev := st.s.Current()
	if e := st.acc.Accumulate(ev); e != nil {
		st.err = e
		st.ended = true
		st.finishLocked(statusOK, st.errLocked())
		return false
	}
	st.cur = translate(ev)
	return true
}

func (st *sseStream) Event() StreamEvent {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur
}

func (st *sseStream) Err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.errLocked()
}

func (st *sseStream) errLocked() error {
	if st.err != nil {
		return st.err
	}
	return classify(st.s.Err())
}

// Usage — накопленные к этому моменту токены. После обрыва или Close именно их
// тарифицирует API, так что потребителю есть за что выставить счёт.
func (st *sseStream) Usage() Usage {
	st.mu.Lock()
	defer st.mu.Unlock()
	return mapUsage(st.acc.Usage)
}

// Final — итоговый ответ дренированного стрима. При ошибке отдаёт её вместе с
// частичным ответом, накопленным после message_start (nil, если не пришло
// ничего); до дрейна — ErrStreamNotDrained, учёт брошенного стрима читают из Usage.
func (st *sseStream) Final() (*Response, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := st.errLocked(); err != nil {
		if st.acc.ID == "" {
			return nil, err
		}
		return st.client.buildResponse(&st.acc, st.model), err
	}
	if !st.ended {
		return nil, ErrStreamNotDrained
	}
	return st.client.buildResponse(&st.acc, st.model), nil
}

// Close до исчерпания SSE — брошенный стрим: метрики пишутся с накопленным usage
// под status=abandoned. Вызывается из любой горутины, в том числе пока другая
// читает Next: соединение рвётся сразу, учёт пишет проснувшийся читатель.
func (st *sseStream) Close() error {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	reading := st.inNext
	st.mu.Unlock()

	err := st.s.Close()
	if !reading {
		st.mu.Lock()
		st.finishLocked(statusAbandoned, st.errLocked())
		st.mu.Unlock()
	}
	return err
}

// finishLocked пишет учёт вызова ровно один раз. err — ошибка, с которой стрим
// завершился: она переводит вызов в status=error и в спан.
func (st *sseStream) finishLocked(status string, err error) {
	if st.done {
		return
	}
	st.done = true

	model := st.acc.Model
	if model == "" {
		model = st.model
	}
	u := mapUsage(st.acc.Usage)
	cost, known := st.client.costOf(model, u)
	if err != nil {
		recordSpanError(st.span, err)
	}
	annotateSpan(st.span, &Response{
		ID:         st.acc.ID,
		Model:      model,
		StopReason: StopReason(st.acc.StopReason),
		Usage:      u,
	})
	st.span.End()

	st.client.observe(st.ctx, st.start,
		callResult{op: opStream, model: st.model, status: status, priced: known, usage: u, cost: cost},
		err)
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
