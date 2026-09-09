// Package anthropictest — тест-двойники anthropickit для потребителя: настраиваемый
// Stub реализации anthropickit.API и скриптуемый Stream, всё без сети.
package anthropictest

import (
	"context"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"

	"git.stit.tech/stit-core/golibinfra/anthropickit"
)

// DefaultModel — что отдаёт Stub.DefaultModel, если поле не задано.
const DefaultModel = "claude-opus-5"

// Stub — реализация anthropickit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	CompleteFunc     func(ctx context.Context, req anthropickit.Request) (*anthropickit.Response, error)
	StreamFunc       func(ctx context.Context, req anthropickit.Request) anthropickit.Stream
	CountTokensFunc  func(ctx context.Context, req anthropickit.Request) (int64, error)
	DefaultModelName string
	RawClient        anthropic.Client
}

var _ anthropickit.API = (*Stub)(nil)

// Complete отдаёт CompleteFunc, иначе безопасный дефолтный ответ.
func (s *Stub) Complete(ctx context.Context, req anthropickit.Request) (*anthropickit.Response, error) {
	if s.CompleteFunc != nil {
		return s.CompleteFunc(ctx, req)
	}
	return stubResponse(), nil
}

// Stream отдаёт StreamFunc, иначе ScriptStream с одной text-дельтой и Done.
func (s *Stub) Stream(ctx context.Context, req anthropickit.Request) anthropickit.Stream {
	if s.StreamFunc != nil {
		return s.StreamFunc(ctx, req)
	}
	return ScriptStream(
		stubResponse(),
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "stub"},
		anthropickit.StreamEvent{Type: anthropickit.EventDone},
	)
}

// CountTokens отдаёт CountTokensFunc, иначе 0.
func (s *Stub) CountTokens(ctx context.Context, req anthropickit.Request) (int64, error) {
	if s.CountTokensFunc != nil {
		return s.CountTokensFunc(ctx, req)
	}
	return 0, nil
}

// DefaultModel отдаёт DefaultModelName, а при пустом — дефолт пакета.
func (s *Stub) DefaultModel() string {
	if s.DefaultModelName != "" {
		return s.DefaultModelName
	}
	return DefaultModel
}

// Raw отдаёт RawClient как есть — нулевое значение, если он не задан.
func (s *Stub) Raw() anthropic.Client { return s.RawClient }

func stubResponse() *anthropickit.Response {
	return &anthropickit.Response{
		Role:       anthropickit.RoleAssistant,
		StopReason: anthropickit.StopEndTurn,
		Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "stub"}},
		CostKnown:  true,
	}
}

// scriptStream проигрывает заданные события и повторяет дрейн-контракт боевого
// sseStream, включая семантику ошибки и Usage.
type scriptStream struct {
	events []anthropickit.StreamEvent
	final  *anthropickit.Response
	err    error

	// Контракт боевого Stream: Close и Usage вызываются из любой горутины.
	mu     sync.Mutex
	i      int
	closed bool
}

// ScriptStream — anthropickit.Stream из готового списка событий и финала.
func ScriptStream(final *anthropickit.Response, events ...anthropickit.StreamEvent) anthropickit.Stream {
	return &scriptStream{events: events, final: final}
}

// ScriptStreamErr — стрим, который проигрывает события и затем ломается: Next
// становится false, Err и Final отдают err. Так выглядит самый частый боевой
// сбой — обрыв посреди генерации.
func ScriptStreamErr(err error, events ...anthropickit.StreamEvent) anthropickit.Stream {
	return &scriptStream{events: events, err: err}
}

// ErrStream — anthropickit.Stream, который сразу в ошибке.
func ErrStream(err error) anthropickit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

// Event — текущее событие; до первого Next это нулевое событие.
func (s *scriptStream) Event() anthropickit.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i == 0 {
		return anthropickit.StreamEvent{}
	}
	return s.events[s.i-1]
}

// Err отдаёт заданную ошибку после того, как скрипт проигран: боевой стрим
// ломается так же — после того, как Next вернул false.
func (s *scriptStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errLocked()
}

func (s *scriptStream) errLocked() error {
	if s.i < len(s.events) && !s.closed {
		return nil
	}
	return s.err
}

// Usage — учёт, накопленный проигранными событиями, как у боевого стрима после
// обрыва.
func (s *scriptStream) Usage() anthropickit.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u anthropickit.Usage
	for _, ev := range s.events[:s.i] {
		if ev.Usage != nil {
			u = *ev.Usage
		}
	}
	return u
}

// Final повторяет sseStream: заданная ошибка важнее, недоигранный скрипт даёт
// ErrStreamNotDrained.
func (s *scriptStream) Final() (*anthropickit.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errLocked(); err != nil {
		return s.final, err
	}
	if s.i < len(s.events) {
		return nil, anthropickit.ErrStreamNotDrained
	}
	return s.final, nil
}

func (s *scriptStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
