// Package anthropictest — тест-двойники anthropickit для потребителя: настраиваемый
// Stub реализации anthropickit.API и скриптуемый Stream, всё без сети.
package anthropictest

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"

	"git.stit.tech/stit-core/golibinfra/anthropickit"
)

// Stub — реализация anthropickit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	CompleteFunc    func(ctx context.Context, req anthropickit.Request) (*anthropickit.Response, error)
	StreamFunc      func(ctx context.Context, req anthropickit.Request) anthropickit.Stream
	CountTokensFunc func(ctx context.Context, req anthropickit.Request) (int64, error)
	RawClient       anthropic.Client
}

var _ anthropickit.API = (*Stub)(nil)

func (s *Stub) Complete(ctx context.Context, req anthropickit.Request) (*anthropickit.Response, error) {
	if s.CompleteFunc != nil {
		return s.CompleteFunc(ctx, req)
	}
	return &anthropickit.Response{
		Role:       anthropickit.RoleAssistant,
		StopReason: anthropickit.StopEndTurn,
		Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "stub"}},
		CostKnown:  true,
	}, nil
}

func (s *Stub) Stream(ctx context.Context, req anthropickit.Request) anthropickit.Stream {
	if s.StreamFunc != nil {
		return s.StreamFunc(ctx, req)
	}
	final := &anthropickit.Response{
		Role:       anthropickit.RoleAssistant,
		StopReason: anthropickit.StopEndTurn,
		Content:    []anthropickit.Block{anthropickit.TextBlock{Text: "stub"}},
		CostKnown:  true,
	}
	return ScriptStream(
		final,
		anthropickit.StreamEvent{Type: anthropickit.EventTextDelta, Text: "stub"},
		anthropickit.StreamEvent{Type: anthropickit.EventDone},
	)
}

func (s *Stub) CountTokens(ctx context.Context, req anthropickit.Request) (int64, error) {
	if s.CountTokensFunc != nil {
		return s.CountTokensFunc(ctx, req)
	}
	return 0, nil
}

func (s *Stub) Raw() anthropic.Client { return s.RawClient }

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []anthropickit.StreamEvent
	final  *anthropickit.Response
	err    error
	i      int
}

// ScriptStream — anthropickit.Stream из готового списка событий и финала.
func ScriptStream(final *anthropickit.Response, events ...anthropickit.StreamEvent) anthropickit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — anthropickit.Stream, который сразу в ошибке.
func ErrStream(err error) anthropickit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() anthropickit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*anthropickit.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
