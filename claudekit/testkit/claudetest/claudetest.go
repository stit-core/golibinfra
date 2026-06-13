// Package claudetest — тест-двойники claudekit для потребителя: настраиваемый
// Stub реализации claudekit.API и скриптуемый Stream, всё без сети.
package claudetest

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"

	"git.stit.tech/stit-core/golibinfra/claudekit"
)

// Stub — реализация claudekit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	CompleteFunc    func(ctx context.Context, req claudekit.Request) (*claudekit.Response, error)
	StreamFunc      func(ctx context.Context, req claudekit.Request) claudekit.Stream
	CountTokensFunc func(ctx context.Context, req claudekit.Request) (int64, error)
	RawClient       anthropic.Client
}

var _ claudekit.API = (*Stub)(nil)

func (s *Stub) Complete(ctx context.Context, req claudekit.Request) (*claudekit.Response, error) {
	if s.CompleteFunc != nil {
		return s.CompleteFunc(ctx, req)
	}
	return &claudekit.Response{
		Role:       claudekit.RoleAssistant,
		StopReason: claudekit.StopEndTurn,
		Content:    []claudekit.Block{claudekit.TextBlock{Text: "stub"}},
	}, nil
}

func (s *Stub) Stream(ctx context.Context, req claudekit.Request) claudekit.Stream {
	if s.StreamFunc != nil {
		return s.StreamFunc(ctx, req)
	}
	final := &claudekit.Response{
		Role:       claudekit.RoleAssistant,
		StopReason: claudekit.StopEndTurn,
		Content:    []claudekit.Block{claudekit.TextBlock{Text: "stub"}},
	}
	return ScriptStream(
		final,
		claudekit.StreamEvent{Type: claudekit.EventTextDelta, Text: "stub"},
		claudekit.StreamEvent{Type: claudekit.EventDone},
	)
}

func (s *Stub) CountTokens(ctx context.Context, req claudekit.Request) (int64, error) {
	if s.CountTokensFunc != nil {
		return s.CountTokensFunc(ctx, req)
	}
	return 0, nil
}

func (s *Stub) Raw() anthropic.Client { return s.RawClient }

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []claudekit.StreamEvent
	final  *claudekit.Response
	err    error
	i      int
}

// ScriptStream — claudekit.Stream из готового списка событий и финала.
func ScriptStream(final *claudekit.Response, events ...claudekit.StreamEvent) claudekit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — claudekit.Stream, который сразу в ошибке.
func ErrStream(err error) claudekit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() claudekit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*claudekit.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
