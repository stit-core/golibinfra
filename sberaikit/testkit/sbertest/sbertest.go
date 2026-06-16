// Package sbertest — тест-двойники sberaikit для потребителя: настраиваемый Stub
// реализации sberaikit.API и скриптуемый Stream, всё без сети.
package sbertest

import (
	"context"
	"io"
	"net/http"

	"git.stit.tech/stit-core/golibinfra/sberaikit"
)

// Stub — реализация sberaikit.API. Подменяй *Func-поля для нужного поведения;
// иначе отдаёт безопасные дефолты.
type Stub struct {
	ChatFunc        func(ctx context.Context, req sberaikit.ChatRequest) (*sberaikit.ChatResponse, error)
	ChatStreamFunc  func(ctx context.Context, req sberaikit.ChatRequest) sberaikit.Stream
	EmbeddingsFunc  func(ctx context.Context, req sberaikit.EmbeddingsRequest) (*sberaikit.EmbeddingsResponse, error)
	FileContentFunc func(ctx context.Context, fileID string) ([]byte, string, error)
	RawFunc         func(ctx context.Context, method, path string, body io.Reader, hdr http.Header) (*http.Response, error)
}

var _ sberaikit.API = (*Stub)(nil)

func (s *Stub) Chat(ctx context.Context, req sberaikit.ChatRequest) (*sberaikit.ChatResponse, error) {
	if s.ChatFunc != nil {
		return s.ChatFunc(ctx, req)
	}
	return &sberaikit.ChatResponse{
		Object: "chat.completion",
		Choices: []sberaikit.Choice{{
			Index:        0,
			Message:      sberaikit.Message{Role: sberaikit.RoleAssistant, Content: "stub"},
			FinishReason: sberaikit.FinishStop,
		}},
	}, nil
}

func (s *Stub) ChatStream(ctx context.Context, req sberaikit.ChatRequest) sberaikit.Stream {
	if s.ChatStreamFunc != nil {
		return s.ChatStreamFunc(ctx, req)
	}
	final := &sberaikit.ChatResponse{
		Object: "chat.completion",
		Choices: []sberaikit.Choice{{
			Index:        0,
			Message:      sberaikit.Message{Role: sberaikit.RoleAssistant, Content: "stub"},
			FinishReason: sberaikit.FinishStop,
		}},
	}
	return ScriptStream(
		final,
		sberaikit.StreamEvent{Content: "stub", FinishReason: sberaikit.FinishStop},
	)
}

func (s *Stub) Embeddings(ctx context.Context, req sberaikit.EmbeddingsRequest) (*sberaikit.EmbeddingsResponse, error) {
	if s.EmbeddingsFunc != nil {
		return s.EmbeddingsFunc(ctx, req)
	}
	return &sberaikit.EmbeddingsResponse{
		Object: "list",
		Data:   []sberaikit.Embedding{{Object: "embedding", Embedding: []float64{0}, Index: 0}},
	}, nil
}

func (s *Stub) FileContent(ctx context.Context, fileID string) ([]byte, string, error) {
	if s.FileContentFunc != nil {
		return s.FileContentFunc(ctx, fileID)
	}
	return []byte("stub-bytes"), "application/jpg", nil
}

func (s *Stub) Raw(ctx context.Context, method, path string, body io.Reader, hdr http.Header) (*http.Response, error) {
	if s.RawFunc != nil {
		return s.RawFunc(ctx, method, path, body, hdr)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(nil)}, nil
}

// scriptStream проигрывает заранее заданные события и финальный ответ.
type scriptStream struct {
	events []sberaikit.StreamEvent
	final  *sberaikit.ChatResponse
	err    error
	i      int
}

// ScriptStream — sberaikit.Stream из готового списка событий и финала.
func ScriptStream(final *sberaikit.ChatResponse, events ...sberaikit.StreamEvent) sberaikit.Stream {
	return &scriptStream{events: events, final: final}
}

// ErrStream — sberaikit.Stream, который сразу в ошибке.
func ErrStream(err error) sberaikit.Stream { return &scriptStream{err: err} }

func (s *scriptStream) Next() bool {
	if s.err != nil || s.i >= len(s.events) {
		return false
	}
	s.i++
	return true
}

func (s *scriptStream) Event() sberaikit.StreamEvent { return s.events[s.i-1] }

func (s *scriptStream) Err() error { return s.err }

func (s *scriptStream) Final() (*sberaikit.ChatResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.final, nil
}

func (s *scriptStream) Close() error { return nil }
